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
	Duration  int    // 秒
	ProbedAt  time.Time
}

// probeMediaInfo 用镜像内置的 ffprobe 读直链头部，解析主视频流与主音轨
func probeMediaInfo(directURL string) (*probeResult, error) {
	// -analyzeduration/-probesize 限制读取量：只拉头部数据，不下载全文件
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
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

	// 用探测结果生成规范名：原名基名 + 探测画质段 + 原扩展名
	ext := pathExt(task.FileName)
	base := strings.TrimSuffix(task.FileName, ext)
	newName := fmt.Sprintf("%s.%s.%s.%s%s", base, probe.Pix, probe.Video, probe.Audio, ext)
	if probe.Pix == "" {
		newName = fmt.Sprintf("%s.%s.%s%s", base, probe.Video, probe.Audio, ext)
	}
	if newName == task.FileName {
		h.DB.Model(&task).Updates(map[string]interface{}{"status": "skipped", "message": "无可补充信息"})
		return
	}

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
		return
	}
	var exist model.MediaEnrich
	if err := model.DB.Where("file_id = ? AND status IN ?", f.Fid,
		[]string{"pending", "done"}).First(&exist).Error; err == nil {
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

// EnrichQueueAll 批量入队：扫描媒体库中所有缺画质信息的视频。
// POST /enrich/scan —— 手动触发对存量库的补全
func (h *Handler) EnrichQueueAll(c *gin.Context) {
	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("媒体补全扫描")
	defer endTask()

	total, queued, err := h.executeEnrichScan()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "扫描完成", "total_videos": total, "queued": queued})
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
