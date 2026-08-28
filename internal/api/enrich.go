package api

// ==================== 媒体信息补全（ffprobe 探测 → 规范重命名） ====================
//
// 文件名缺分辨率/编码（如 蜘蛛侠.2016.mkv）时：ffprobe 读 115 直链头部
// （只拉几 MB，不下载全文件）拿到真实分辨率/编码/音频，按重命名模板
// 重新命名。队列异步执行（串行限速，不卡整理主流程）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// probeResult ffprobe 探测出的媒体信息（映射为 CMS 风格的画质串片段）
type probeResult struct {
	Pix       string // 1080p / 2160p ...
	Video     string // H264 / H265
	Audio     string // AAC / DDP / DTS...
	Effect    string // HDR / DV（探测出 side_data 时标注）
	Duration  int    // 秒
	ProbedAt  time.Time
}

// probeMediaInfo 用镜像内置的 ffprobe 读直链头部，解析主视频流与主音轨
// probeFileNow 按 pick_code 立即探测（整理内联补全用，包级函数不依赖 Handler）
func probeFileNow(pickCode string) *probeResult {
	var storage model.Storage
	if err := model.DB.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Cookie == "" {
		return nil
	}
	u, _, err := get115DownloadURL(pickCode, storage.Cookie, ua115Unified())
	if err != nil || u == "" {
		return nil
	}
	probe, err := probeMediaInfo(u)
	if err != nil {
		return nil
	}
	return probe
}

func probeMediaInfo(directURL string) (*probeResult, error) {
	// -user_agent：115 直链与签发 UA 绑定，ffprobe 必须用同一 UA 否则被拒（exit 1）
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-user_agent", ua115Unified(),
		"-print_format", "json",
		"-show_streams", "-show_format",
		"-analyzeduration", "10M", "-probesize", "10M",
		directURL)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe 执行失败: %v", err)
	}
	var probe struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			Channels     int    `json:"channels"`
			Duration     string `json:"duration"`
			SideDataList []struct {
				SideDataType string `json:"side_data_type"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fmt.Errorf("ffprobe 输出解析失败: %v", err)
	}

	res := &probeResult{ProbedAt: time.Now()}
	var videoFound, audioFound bool
	for _, st := range probe.Streams {
		if st.CodecType == "video" && !videoFound {
			videoFound = true
			res.Pix = pixFromHeight(st.Height)
			res.Video = videoCodecLabel(st.CodecName)
			// HDR/DV 探测（side_data）：DV 优先于 HDR 标注
			hasDV, hasHDR := false, false
			for _, sd := range st.SideDataList {
				t := strings.ToLower(sd.SideDataType)
				if strings.Contains(t, "dolby") {
					hasDV = true
				}
				if strings.Contains(t, "hdr") && !strings.Contains(t, "dolby") {
					hasHDR = true
				}
			}
			if hasDV {
				res.Effect = "DV"
			} else if hasHDR {
				res.Effect = "HDR"
			}
		}
		if st.CodecType == "audio" && !audioFound {
			audioFound = true
			res.Audio = audioCodecLabel(st.CodecName, st.Channels)
		}
	}
	if !videoFound {
		return nil, fmt.Errorf("未找到视频流")
	}
	if d, perr := parseDuration(probe.Format.Duration); perr == nil {
		res.Duration = int(d)
	}
	return res, nil
}

// pixFromHeight 高度 → 分辨率标签（与 CMS resource_pix 对齐）
func pixFromHeight(h int) string {
	switch {
	case h >= 2000:
		return "2160p"
	case h >= 1000:
		return "1080p"
	case h >= 700:
		return "720p"
	case h >= 400:
		return "480p"
	default:
		return ""
	}
}

// videoCodecLabel 编码名 → 命名习惯标签
func videoCodecLabel(c string) string {
	switch strings.ToLower(c) {
	case "hevc":
		return "H265"
	case "h264", "avc":
		return "H264"
	case "av1":
		return "AV1"
	case "mpeg2video":
		return "MPEG2"
	case "vp9":
		return "VP9"
	default:
		return strings.ToUpper(c)
	}
}

// audioCodecLabel 音频编码 + 声道 → 命名习惯标签
func audioCodecLabel(c string, channels int) string {
	base := strings.ToLower(c)
	var label string
	switch base {
	case "aac":
		label = "AAC"
	case "eac3":
		label = "DDP"
	case "ac3":
		label = "DD"
	case "dts":
		label = "DTS"
	case "truehd":
		label = "TrueHD"
	case "flac":
		label = "FLAC"
	default:
		label = strings.ToUpper(c)
	}
	if channels >= 8 && (base == "eac3" || base == "ac3" || base == "truehd") {
		label += ".Atmos" // 7.1 常见 Atmos；近似标注
	}
	return label
}

func parseDuration(s string) (float64, error) {
	var d float64
	_, err := fmt.Sscanf(s, "%f", &d)
	return d, err
}

// enrichNeedsProbe 文件名是否缺可探测的画质信息（分辨率/编码都解析不到）
func enrichNeedsProbe(fileName string) bool {
	ri := ParseResourceInfo(fileName)
	return ri.Pix == "" && ri.VideoEncode == ""
}

// StartEnrichWorker 补全队列 worker：串行处理 pending 任务，
// 每个之间 30 秒间隔（直链探测虽只拉头部，仍保持温和节奏）
func StartEnrichWorker(h *Handler) {
	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(30 * time.Second):
			}
			h.enrichProcessOne()
		}
	}()
	log.Println("[补全] 队列已启动（每 30 秒处理一个任务）")
}

// enrichProcessOne 取一个 pending 任务处理
func (h *Handler) enrichProcessOne() {
	var task model.MediaEnrich
	if err := h.DB.Where("status = ? AND attempts < 3", "pending").Order("id").First(&task).Error; err != nil {
		return // 队列空
	}
	h.DB.Model(&task).Update("attempts", task.Attempts+1)

	// 直链（Cookie 通道，按统一 UA 签发）
	db := h.DB
	var storage model.Storage
	if err := db.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Cookie == "" {
		h.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "message": "115 未绑定"})
		return
	}
	u, _, err := get115DownloadURL(task.PickCode, storage.Cookie, ua115Unified())
	if err != nil || u == "" {
		h.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "message": "取直链失败: " + err.Error()})
		return
	}

	probe, err := probeMediaInfo(u)
	if err != nil {
		h.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "message": err.Error()})
		log.Printf("[补全] ✗ 探测失败 %s: %v", task.FileName, err)
		return
	}

	// 决策引擎：按八情形矩阵与用户策略决定 改名/跳过/存疑替换
	ext := pathExt(task.FileName)
	base := strings.TrimSuffix(task.FileName, ext)
	action, reason := enrichDecide(task.FileName, probe, loadEnrichPolicy())
	if action != "rename" {
		h.DB.Model(&task).Updates(map[string]interface{}{"status": "skipped", "message": reason})
		vlog("[补全] ○ %s: %s", task.FileName, reason)
		return
	}
	newName := buildEnrichedName(base, ext, probe)

	ops, err := h.newPan115Ops()
	if err == nil {
		if rerr := ops.rename(task.FileID, newName); rerr != nil {
			h.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "message": "重命名失败: " + rerr.Error()})
			log.Printf("[补全] ✗ 重命名失败 %s: %v", task.FileName, rerr)
			return
		}
	}
	h.DB.Model(&task).Updates(map[string]interface{}{"status": "done", "message": probe.Pix + " " + probe.Video + " " + probe.Audio})
	log.Printf("[补全] ✓ %s → %s", task.FileName, newName)

	// 台账 rel_path 同步（本地 strm 无需改名：strm 名与源文件解耦，内容按 pickcode 取流）
}

// enrichQueueTask 入队（整理识别成功但缺画质信息时调用；同文件不重复入队）
func enrichQueueTask(f remoteFile) {
	if model.DB == nil || f.Fid == "" || f.PickCode == "" {
		log.Printf("[补全] ○ 跳过入队（fid=%q pick=%q 无效）", f.Fid, f.PickCode)
		return
	}
	var exist model.MediaEnrich
	if err := model.DB.Where("file_id = ? AND status IN ?", f.Fid,
		[]string{"pending", "done"}).First(&exist).Error; err == nil {
		log.Printf("[补全] ○ 已在队列（%s status=%s）", exist.FileName, exist.Status)
		return // 已在队列或已完成
	}
	model.DB.Create(&model.MediaEnrich{
		FileID: f.Fid, PickCode: f.PickCode, FileName: f.Name, Status: "pending",
	})
	log.Printf("[补全] ▶ 入队: %s（文件名缺画质信息，将探测后规范命名）", f.Name)
}

// executeEnrichScan 扫描媒体库入队（HTTP 与企微指令共用），返回 (视频总数, 入队数, 错误)
func (h *Handler) executeEnrichScan() (int, int, error) {
	orgCfg, err := h.loadOrgConfig()
	if err != nil {
		return 0, 0, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return 0, 0, err
	}
	filter := &syncFilter{
		videoExts: buildExtSet([]string{"mp4", "mkv", "ts", "avi", "mov", "rmvb", "webm", "flv", "m2ts", "wmv", "mpg", "iso"}),
		assetExts: map[string]bool{},
	}
	skipCids, _ := h.orgSkipCids(orgCfg.Library)
	var videos []remoteFile
	libName := ""
	if cookie, err := h.get115Cookie(); err == nil {
		if info, err := get115DirInfo(cookie, orgCfg.Library); err == nil {
			libName = info.n
		}
	}
	if err := walk115Dir(ops, orgCfg.Library, libName, &videos, nil, filter, skipCids); err != nil {
		return 0, 0, err
	}
	queued := 0
	for _, v := range videos {
		if !enrichNeedsProbe(v.Name) {
			continue
		}
		enrichQueueTask(v)
		queued++
	}
	log.Printf("[补全] ▶ 存量扫描完成: 共 %d 个视频，%d 个缺画质信息已入队", len(videos), queued)
	return len(videos), queued, nil
}

// EnrichList 队列状态。GET /enrich/list
func (h *Handler) EnrichList(c *gin.Context) {
	var tasks []model.MediaEnrich
	h.DB.Order("id DESC").Limit(100).Find(&tasks)
	counts := map[string]int64{}
	for _, st := range []string{"pending", "done", "failed", "skipped"} {
		var n int64
		h.DB.Model(&model.MediaEnrich{}).Where("status = ?", st).Count(&n)
		counts[st] = n
	}
	c.JSON(http.StatusOK, gin.H{"data": tasks, "counts": counts})
}


// ---- 补全策略（UI 可配） ----

// enrichPolicy 用户策略：各情形的处置（rename=按探测改 / keep=保留原名）
type enrichPolicy struct {
	Enabled bool   `json:"enabled"`  // 总开关（关闭时自动入队与扫描均不工作）
	Mode    string `json:"mode"`     // conservative 保守 / standard 标准 / aggressive 激进
	// 逐情形策略（空 = 跟随 mode 默认；rename/keep 二选一）
	Missing      string `json:"missing"`       // 情形1：名缺+探测有 → 默认 rename
	Match        string `json:"match"`         // 情形2：名实一致 → 固定 keep（不可改，展示用）
	ConflictLow  string `json:"conflict_low"`  // 情形3：名1080探测2160 → 标准默认 rename
	ConflictHigh string `json:"conflict_high"` // 情形4：名2160探测1080 → 标准默认 rename
	ProbeFail    string `json:"probe_fail"`    // 情形5：名有+探测失败 → 固定 keep（探测都失败无从改）
	FullNamed    string `json:"full_named"`    // 情形7：命名已完整 → 保守/标准默认 keep，激进 rename
}

// loadEnrichPolicy 读取补全策略（随 org-basic 配置存储于 enrich 字段；
// 未配置=关闭+标准默认）
func loadEnrichPolicy() enrichPolicy {
	p := enrichPolicy{Enabled: false, Mode: "standard"}
	v := modelSettingValue("org-basic")
	if v == "" {
		return p
	}
	var wrapped struct {
		Enrich enrichPolicy `json:"enrich"`
	}
	if json.Unmarshal([]byte(v), &wrapped) != nil || wrapped.Enrich.Mode == "" && !wrapped.Enrich.Enabled {
		return p
	}
	p = wrapped.Enrich
	if p.Mode == "" {
		p.Mode = "standard"
	}
	return p
}

// pixTier 分辨率档位（跨档防御用）：极端跳档视为探测可疑
func pixTier(p string) int {
	switch p {
	case "2160p":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	}
	return 0
}

// enrichDecide 决策矩阵。返回 action: rename/keep，reason 供日志与队列 message
func enrichDecide(fileName string, probe *probeResult, policy enrichPolicy) (string, string) {
	if probe == nil {
		return "keep", "探测失败，保留原名"
	}
	ri := ParseResourceInfo(fileName)
	claimedPix, probedPix := ri.Pix, probe.Pix

	// 情形2：名实一致（pix 都有且相同）→ 跳过
	if claimedPix != "" && probedPix != "" && claimedPix == probedPix {
		return "keep", "名实相符"
	}
	// 情形1：名缺 + 探测有 → 补充
	if claimedPix == "" && probedPix != "" {
		if policy.Missing == "keep" {
			return "keep", "策略：名缺不改（保守设置）"
		}
		return "rename", "补充缺失画质"
	}
	// 情形5/6：探测无结果 → 保留（声明是唯一信息）
	if probedPix == "" {
		return "keep", "探测无画质信息，保留原名"
	}
	// 情形3/4：名实冲突
	if claimedPix != "" {
		// 跨档防御：极端跳档（差 ≥2 档）视为探测可疑
		ct, pt := pixTier(claimedPix), pixTier(probedPix)
		if ct > 0 && pt > 0 && absInt(ct-pt) >= 2 {
			return "keep", fmt.Sprintf("跨档差异过大（声明 %s 探测 %s），疑似探测异常，保留", claimedPix, probedPix)
		}
		// 完整命名（有来源+发布组）→ 情形7
		if ri.Type != "" && ri.Team != "" {
			if policy.FullNamed == "rename" || (policy.FullNamed == "" && policy.Mode == "aggressive") {
				return "rename", fmt.Sprintf("完整命名但名实冲突（%s→%s），激进策略按探测改", claimedPix, probedPix)
			}
			return "keep", fmt.Sprintf("命名完整（%s），名实差异仅记录：%s vs %s", fileName, claimedPix, probedPix)
		}
		// 普通冲突：按配置/模式
		action, label := policy.ConflictLow, "低报"
		if pixTier(probedPix) < pixTier(claimedPix) {
			action, label = policy.ConflictHigh, "高报"
		}
		if action == "" {
			// 标准模式默认 rename（探测为事实）；保守模式 keep
			if policy.Mode == "conservative" {
				action = "keep"
			} else {
				action = "rename"
			}
		}
		if action == "keep" {
			return "keep", fmt.Sprintf("策略保留：名 %s 探测 %s（%s）", claimedPix, probedPix, label)
		}
		return "rename", fmt.Sprintf("名实冲突以探测为准（%s→%s，%s）", claimedPix, probedPix, label)
	}
	return "keep", "未归类情形，保守保留"
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// buildEnrichedName 重建规范名：保留原基名（含片名/集数/年份/来源/组等
// 已有信息段），替换/追加探测画质段。原基名里已有的同位段先剔除再补
func buildEnrichedName(base, ext string, probe *probeResult) string {
	// 已有画质段中与探测冲突的部分（分辨率/编码）从基名剔除，其余保留
	ri := ParseResourceInfo(base)
	cleanBase := base
	if ri.Pix != "" {
		cleanBase = stripToken(cleanBase, ri.Pix)
	}
	if ri.VideoEncode != "" {
		cleanBase = stripToken(cleanBase, ri.VideoEncode)
	}
	cleanBase = strings.Trim(cleanBase, " .-_")
	var parts []string
	for _, p := range []string{probe.Pix, probe.Effect, probe.Video, probe.Audio} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return base + ext
	}
	return cleanBase + "." + strings.Join(parts, ".") + ext
}

// stripToken 从字符串中移除一个 token（含其紧邻的分隔符）
func stripToken(s, token string) string {
	if token == "" {
		return s
	}
	re := regexp.MustCompile(`(?i)[.\s_-]*` + regexp.QuoteMeta(token) + `[.\s_-]*`)
	return re.ReplaceAllString(s, ".")
}
