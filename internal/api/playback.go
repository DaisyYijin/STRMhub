package api

// ==================== 多端播放 · 小号播放（播放账号池） ====================
//
// 主号只做转存/整理/同步（写操作），播放取直链走小号账号池——播放是
// 最高频、最容易触发 115 风控的操作，剥离后主号风险大幅降低，多端并发
// 时各设备分摊不同小号（流量/风控隔离）。
//
// 小号访问不到主号网盘（pickcode 是账号内标识），通行解法是"分享+秒传"：
//
//	主号媒体库文件 ──share/send──▶ 分享链接
//	小号 receive 到镜像目录（秒传，不耗下载流量，只占小号空间）
//	小号目录树与主号台账 rel_path 对齐 → 维护 RelPath→小号PickCode 映射
//	播放时：台账 pickcode → RelPath → 小号 pickcode → 小号 Cookie 取直链
//
// 同步为幂等增量：以 SyncedFile 台账（kind=video）为准，缺什么补什么。

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"strmhub/internal/config"
	"strmhub/internal/model"
)

// playbackAlt 小号账号（setting key "playback"）
type playbackAlt struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Cookie  string `json:"cookie"`
	Enabled bool   `json:"enabled"`
	Nick    string `json:"nick"` // 验证时缓存的账号昵称
	RootCid string `json:"root_cid"`
}

type playbackCfg struct {
	Mode    string        `json:"mode"`    // main=主号播放 alt=小号池
	Alts    []playbackAlt `json:"alts"`    // 小号池
	Routing string        `json:"routing"` // device=设备绑定 round=轮询分摊
}

var (
	playbackMu    sync.Mutex
	playbackCfgV  *playbackCfg
	playbackCfgAt time.Time
)

func loadPlaybackCfg() *playbackCfg {
	playbackMu.Lock()
	defer playbackMu.Unlock()
	if playbackCfgV != nil && time.Since(playbackCfgAt) < 5*time.Second {
		return playbackCfgV
	}
	cfg := &playbackCfg{Mode: "main", Routing: "device"}
	if v := settingValueCompat("playback"); v != "" {
		_ = json.Unmarshal([]byte(v), cfg)
	}
	if cfg.Mode == "" {
		cfg.Mode = "main"
	}
	if cfg.Routing == "" {
		cfg.Routing = "device"
	}
	playbackCfgV = cfg
	playbackCfgAt = time.Now()
	return cfg
}

func savePlaybackCfg(c *playbackCfg) error {
	b, _ := json.Marshal(c)
	if err := notifyConfigSource.SaveSetting("playback", string(b)); err != nil {
		return err
	}
	playbackMu.Lock()
	playbackCfgV = nil
	playbackMu.Unlock()
	return nil
}

// altOps 小号专属操作通道（Cookie 通道，OpenAPI 是主号的）
func altOps(alt *playbackAlt) *pan115Ops {
	return &pan115Ops{cookie: alt.Cookie}
}

// altVerifyCookie 验证小号 Cookie（昵称+uid），返回 (uid, 昵称, 错误)
func altVerifyCookie(cookie string) (string, string, error) {
	body, err := httpGet115Full("https://webapi.115.com/user/infos", nil, cookie, ua115Unified(), 15*time.Second, nil)
	if err != nil {
		return "", "", err
	}
	var r struct {
		State bool `json:"state"`
		Data  struct {
			UID      any    `json:"uid"`
			UserName string `json:"user_name"`
			UserID   any    `json:"user_id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return "", "", fmt.Errorf("Cookie 无效（state=%v）", truncateStr(string(body), 80))
	}
	uid := fmt.Sprint(r.Data.UID)
	if uid == "" || uid == "<nil>" {
		uid = fmt.Sprint(r.Data.UserID)
	}
	return uid, r.Data.UserName, nil
}

// ---- HTTP 处理器 ----

// PlaybackGetConfig GET /playback/config → 配置 + 各小号映射覆盖数
func (h *Handler) PlaybackGetConfig(c *gin.Context) {
	cfg := loadPlaybackCfg()
	type altOut struct {
		playbackAlt
		Covered int64  `json:"covered"` // 映射表已覆盖的文件数
		Missing int64  `json:"missing"` // 台账有但小号没有的文件数
		LastErr string `json:"last_err"`
	}
	out := make([]altOut, 0, len(cfg.Alts))
	for _, a := range cfg.Alts {
		o := altOut{playbackAlt: playbackAlt{
			ID: a.ID, Name: a.Name, Enabled: a.Enabled, Nick: a.Nick, RootCid: a.RootCid,
		}}
		model.DB.Model(&model.PlaybackAltFile{}).Where("account_id = ?", a.ID).Count(&o.Covered)
		o.Missing = playbackMissingCount(a.ID)
		out = append(out, o)
	}
	// 脱敏：Cookie 不回传
	c.JSON(http.StatusOK, gin.H{"cfg": gin.H{
		"mode": cfg.Mode, "routing": cfg.Routing, "alts": out,
	}})
}

// PlaybackAddAlt POST /playback/alt/add {name, cookie} → 验证并加入账号池
func (h *Handler) PlaybackAddAlt(c *gin.Context) {
	var req struct {
		Name   string `json:"name"`
		Cookie string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Cookie) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 Cookie"})
		return
	}
	cookie := strings.TrimSpace(req.Cookie)
	uid, nick, err := altVerifyCookie(cookie)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cookie 验证失败: " + err.Error()})
		return
	}
	// 主号判重：小号不能是主号自己
	if mainCookie, err := h.get115Cookie(); err == nil && mainCookie != "" {
		mainUID := ""
		mainUID, _, _ = altVerifyCookie(mainCookie)
		if uid != "" && uid == mainUID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该 Cookie 是主号自己，请填小号的"})
			return
		}
	}
	cfg := loadPlaybackCfg()
	for _, a := range cfg.Alts {
		if auid, _, _ := altVerifyCookie(a.Cookie); uid != "" && auid == uid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "该小号已在账号池中"})
			return
		}
	}
	var maxID int64 = 0
	for _, a := range cfg.Alts {
		if a.ID > maxID {
			maxID = a.ID
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = nick
	}
	cfg.Alts = append(cfg.Alts, playbackAlt{
		ID: maxID + 1, Name: name, Cookie: cookie, Enabled: true, Nick: nick,
	})
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[播放账号] ✓ 小号「%s」已加入账号池（uid=%s）", name, uid)
	c.JSON(http.StatusOK, gin.H{"message": "小号已添加并验证通过（" + nick + "）"})
}

// PlaybackDelAlt POST /playback/alt/del {id} → 移除小号及其映射
func (h *Handler) PlaybackDelAlt(c *gin.Context) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	left := cfg.Alts[:0]
	for _, a := range cfg.Alts {
		if a.ID != req.ID {
			left = append(left, a)
		}
	}
	cfg.Alts = left
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Where("account_id = ?", req.ID).Delete(&model.PlaybackAltFile{})
	c.JSON(http.StatusOK, gin.H{"message": "已移除（映射已清理）"})
}

// PlaybackToggleAlt POST /playback/alt/toggle {id, enabled}
func (h *Handler) PlaybackToggleAlt(c *gin.Context) {
	var req struct {
		ID      int64 `json:"id"`
		Enabled bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	for i := range cfg.Alts {
		if cfg.Alts[i].ID == req.ID {
			cfg.Alts[i].Enabled = req.Enabled
		}
	}
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// PlaybackSaveMode POST /playback/mode {mode, routing}
func (h *Handler) PlaybackSaveMode(c *gin.Context) {
	var req struct {
		Mode    string `json:"mode"`
		Routing string `json:"routing"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPlaybackCfg()
	if req.Mode == "main" || req.Mode == "alt" {
		if req.Mode == "alt" && len(cfg.Alts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "账号池为空，先添加小号"})
			return
		}
		cfg.Mode = req.Mode
	}
	if req.Routing == "device" || req.Routing == "round" {
		cfg.Routing = req.Routing
	}
	if err := savePlaybackCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// ---- 内容同步（分享+秒传） ----

// playbackSyncRunning 同步互斥（同步可能耗时较长）
var playbackSyncRunning atomic.Bool

// PlaybackSync POST /playback/sync → 触发一轮增量镜像
func (h *Handler) PlaybackSync(c *gin.Context) {
	if !playbackSyncRunning.CompareAndSwap(false, true) {
		c.JSON(http.StatusConflict, gin.H{"error": "同步正在进行中"})
		return
	}
	go func() {
		defer playbackSyncRunning.Store(false)
		cfg := loadPlaybackCfg()
		for i := range cfg.Alts {
			if !cfg.Alts[i].Enabled {
				continue
			}
			alt := &cfg.Alts[i]
			if msg, err := h.playbackSyncAlt(alt); err != nil {
				log.Printf("[播放账号] ✗ 小号「%s」同步失败: %v", alt.Name, err)
				playbackSetAltErr(alt.ID, err.Error())
			} else {
				log.Printf("[播放账号] ✓ 小号「%s」%s", alt.Name, msg)
				playbackSetAltErr(alt.ID, "")
			}
		}
	}()
	c.JSON(http.StatusOK, gin.H{"message": "镜像同步已开始，结果见运行日志"})
}

// playbackSyncState 每小号最近一次同步状态（内存态，重启丢失无妨）
var (
	playbackStateMu  sync.Mutex
	playbackStateMap = map[int64]string{}
)

func playbackSetAltErr(id int64, msg string) {
	playbackStateMu.Lock()
	playbackStateMap[id] = msg
	playbackStateMu.Unlock()
}

// playbackMissingCount 台账有但该小号映射未覆盖的文件数
func playbackMissingCount(accountID int64) int64 {
	var covered int64
	model.DB.Model(&model.PlaybackAltFile{}).Where("account_id = ?", accountID).Count(&covered)
	var total int64
	model.DB.Model(&model.SyncedFile{}).Where("kind = ? AND pick_code <> ''", "video").Count(&total)
	n := total - covered
	if n < 0 {
		n = 0
	}
	return n
}

// playbackSyncAlt 单个小号的镜像同步：
//  1. 主号台账（rel_path→pickcode）为基准
//  2. 小号镜像目录树（rel_path→pickcode）
//  3. 缺失文件：主号分享 → 小号 receive 到镜像目录（目录不存在先建）
//  4. 落映射表
func (h *Handler) playbackSyncAlt(alt *playbackAlt) (string, error) {
	if strings.TrimSpace(alt.Cookie) == "" {
		return "", fmt.Errorf("Cookie 为空")
	}
	// 主号基准：台账
	var ledger []model.SyncedFile
	if err := model.DB.Where("kind = ? AND pick_code <> ''", "video").Find(&ledger).Error; err != nil {
		return "", err
	}
	if len(ledger) == 0 {
		return "", fmt.Errorf("同步台账为空（先完成一次全量同步）")
	}
	ledgerMap := map[string]string{} // rel_path → main pickcode
	for _, sf := range ledger {
		ledgerMap[strings.Trim(sf.RelPath, "/")] = sf.PickCode
	}

	// 小号镜像根目录（strmhub_media_alt），不存在则建
	altOpsC := altOps(alt)
	altRoot, err := altEnsureRoot(altOpsC, alt)
	if err != nil {
		return "", err
	}

	// 小号现有目录树
	altTree := map[string]string{} // rel_path → alt pickcode
	walkAltTree(altOpsC, altRoot, "", altTree)

	// 差异：台账有、小号没有 → 需转存
	var missing []string // rel_path 列表
	for rel := range ledgerMap {
		if _, ok := altTree[rel]; !ok {
			missing = append(missing, rel)
		}
	}
	added, transferred := 0, 0
	if len(missing) > 0 {
		sort.Strings(missing)
		// 按目录分组转存（一批一批来，每批一个目录）
		byDir := map[string][]string{}
		for _, rel := range missing {
			dir := relDirOf(rel)
			byDir[dir] = append(byDir[dir], rel)
		}
		dirs := make([]string, 0, len(byDir))
		for d := range byDir {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			select {
			case <-stopCh:
				break
			default:
			}
			rels := byDir[dir]
			// 1) 小号侧建目录（逐级），拿到 cid
			cid, err := altEnsureDir(altOpsC, altRoot, dir)
			if err != nil {
				log.Printf("[播放账号] ○ 目录 %s 创建失败: %v", dir, err)
				continue
			}
			// 2) 主号对这些文件创建分享
			mainCookie, _ := h.get115Cookie()
			fileIDs := make([]string, 0, len(rels))
			for _, rel := range rels {
				if fid := mainFileIDByPick(mainCookie, ledgerMap[rel]); fid != "" {
					fileIDs = append(fileIDs, fid)
				}
			}
			if len(fileIDs) == 0 {
				continue
			}
			shareCode, receiveCode, err := mainShareFiles(mainCookie, fileIDs)
			if err != nil {
				log.Printf("[播放账号] ○ 分享创建失败（%s）: %v", dir, err)
				continue
			}
			// 3) 小号转存（小号 Cookie 走 receive 链路）
			if err := altReceiveShare(alt.Cookie, shareCode, receiveCode, cid); err != nil {
				log.Printf("[播放账号] ○ 转存失败（%s）: %v", dir, err)
				continue
			}
			transferred += len(rels)
			// 4) 重扫该目录更新映射
			time.Sleep(500 * time.Millisecond)
			walkAltDir(altOpsC, altRoot, dir, altTree)
		}
	}

	// 落映射表
	added = playbackSaveMapping(alt.ID, ledgerMap, altTree)
	msg := fmt.Sprintf("镜像同步完成：覆盖 %d，本轮新增 %d（转存 %d 文件）", added, len(missing), transferred)
	return msg, nil
}

// altEnsureRoot 小号镜像根目录（固定名 strmhub_media_alt）
func altEnsureRoot(ops *pan115Ops, alt *playbackAlt) (string, error) {
	if alt.RootCid != "" {
		// 校验还在
		if _, err := get115DirInfo(alt.Cookie, alt.RootCid); err == nil {
			return alt.RootCid, nil
		}
	}
	cid, err := ops.mkdir("0", "strmhub_media_alt")
	if err != nil {
		// 可能已存在：搜一遍
		return "", fmt.Errorf("创建镜像根目录失败: %w", err)
	}
	alt.RootCid = cid
	cfg := loadPlaybackCfg()
	for i := range cfg.Alts {
		if cfg.Alts[i].ID == alt.ID {
			cfg.Alts[i].RootCid = cid
		}
	}
	_ = savePlaybackCfg(cfg)
	return cid, nil
}

// walkAltTree 递归遍历小号镜像树（只收视频关心的全部文件映射）
func walkAltTree(ops *pan115Ops, rootCid, prefix string, out map[string]string) {
	walkAltDir(ops, rootCid, prefix, out)
}

// walkAltDir 递归遍历某个子目录
func walkAltDir(ops *pan115Ops, cid, prefix string, out map[string]string) {
	entries, _, err := ops.listEntries(cid, 0)
	if err != nil {
		return
	}
	var subDirs []struct {
		id   string
		name string
	}
	for _, e := range entries {
		if fmt.Sprint(e["f"]) == "1" {
			// 文件：rel = prefix + 文件名
			name := fmt.Sprint(e["n"])
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if pc := fmt.Sprint(e["pc"]); pc != "" {
				out[rel] = pc
			}
		} else {
			subDirs = append(subDirs, struct {
				id   string
				name string
			}{fmt.Sprint(e["fid"]), fmt.Sprint(e["n"])})
		}
	}
	for _, sd := range subDirs {
		rel := sd.name
		if prefix != "" {
			rel = prefix + "/" + sd.name
		}
		walkAltDir(ops, sd.id, rel, out)
	}
}

// altEnsureDir 逐级确保小号侧目录存在（mkdir115 幂等：重名 115 返回错误但目录已在）
func altEnsureDir(ops *pan115Ops, rootCid, relDir string) (string, error) {
	cur := rootCid
	for _, seg := range strings.Split(relDir, "/") {
		if seg == "" {
			continue
		}
		// 找现有
		found := ""
		dirs, _, _, err := ops.listDirs(cur)
		if err == nil {
			for _, d := range dirs {
				if fmt.Sprint(d["name"]) == seg {
					found = fmt.Sprint(d["cid"])
					break
				}
			}
		}
		if found == "" {
			var err error
			found, err = mkdir115(ops.cookie, cur, seg)
			if err != nil {
				// 重名冲突 = 已存在，重查
				dirs, _, _, err2 := ops.listDirs(cur)
				if err2 == nil {
					for _, d := range dirs {
						if fmt.Sprint(d["name"]) == seg {
							found = fmt.Sprint(d["cid"])
							break
						}
					}
				}
				if found == "" {
					return "", err
				}
			}
		}
		cur = found
		time.Sleep(200 * time.Millisecond) // 目录创建有 115 端延迟
	}
	return cur, nil
}

// mainFileIDByPick 主号 pickcode → file_id（files/getid 反查不可用，用
// 文件 API：webapi files API 按 pickcode 查详情拿 fid）
func mainFileIDByPick(cookie, pickCode string) string {
	body, err := httpGet115Full("https://webapi.115.com/files/getinfo",
		url.Values{"pick_code": {pickCode}}, cookie, ua115Unified(), 15*time.Second, nil)
	if err != nil {
		return ""
	}
	var r struct {
		State bool `json:"state"`
		Data  []struct {
			FileID any `json:"file_id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State || len(r.Data) == 0 {
		return ""
	}
	return fmt.Sprint(r.Data[0].FileID)
}

// mainShareFiles 主号对一批 file_id 创建分享，返回 (share_code, receive_code)
func mainShareFiles(cookie string, fileIDs []string) (string, string, error) {
	if len(fileIDs) == 0 {
		return "", "", fmt.Errorf("无文件")
	}
	form := url.Values{
		"pid":         {"0"},
		"file_ids":    {strings.Join(fileIDs, ",")},
		"is_asc":      {"1"},
		"secret_code": {randomCode(4)},
	}
	body, err := httpPostForm115("https://webapi.115.com/share/send", form, cookie, 15*time.Second)
	if err != nil {
		return "", "", err
	}
	var r struct {
		State bool `json:"state"`
		Data  struct {
			ShareCode   string `json:"share_code"`
			ReceiveCode string `json:"receive_code"`
		} `json:"data"`
		Error    string `json:"error"`
		ErrorMsg string `json:"error_msg"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		msg := r.Error
		if msg == "" {
			msg = r.ErrorMsg
		}
		return "", "", fmt.Errorf("%s: %s", msg, truncateStr(string(body), 100))
	}
	if r.Data.ShareCode == "" || r.Data.ReceiveCode == "" {
		return "", "", fmt.Errorf("响应缺 share_code: %s", truncateStr(string(body), 100))
	}
	return r.Data.ShareCode, r.Data.ReceiveCode, nil
}

func randomCode(n int) string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// playbackSaveMapping 落映射表（全量对账式：删旧插新，事务内）
func playbackSaveMapping(accountID int64, ledgerMap, altTree map[string]string) int {
	type row struct {
		RelPath  string
		PickCode string
	}
	var rows []row
	for rel := range ledgerMap {
		if altPC, ok := altTree[rel]; ok && altPC != "" {
			rows = append(rows, row{rel, altPC})
		}
	}
	model.DB.Where("account_id = ?", accountID).Delete(&model.PlaybackAltFile{})
	if len(rows) == 0 {
		return 0
	}
	batch := make([]model.PlaybackAltFile, 0, len(rows))
	for _, r := range rows {
		batch = append(batch, model.PlaybackAltFile{
			AccountID: accountID, RelPath: r.RelPath, AltPickCode: r.PickCode,
		})
	}
	for i := 0; i < len(batch); i += 500 {
		end := i + 500
		if end > len(batch) {
			end = len(batch)
		}
		model.DB.CreateInBatches(batch[i:end], 200)
	}
	return len(rows)
}

// ---- 播放路由（302 取链接入） ----

// playbackRouteForUA 为一次播放请求选小号：设备绑定（UA 哈希稳定映射）
// 或轮询分摊；返回 nil 表示不用小号（主号）
func playbackRouteForUA(cfg *playbackCfg, ua string) *playbackAlt {
	if cfg.Mode != "alt" || len(cfg.Alts) == 0 {
		return nil
	}
	var enabled []*playbackAlt
	for i := range cfg.Alts {
		if cfg.Alts[i].Enabled && strings.TrimSpace(cfg.Alts[i].Cookie) != "" {
			enabled = append(enabled, &cfg.Alts[i])
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	if cfg.Routing == "round" {
		var n int64
		model.DB.Model(&model.PlaybackDevice{}).Count(&n)
		return enabled[int(n)%len(enabled)]
	}
	// 设备绑定：UA 哈希 → 固定小号（同设备永远同小号，直链缓存键稳定）
	h := fnv.New32a()
	_, _ = h.Write([]byte(ua))
	return enabled[int(h.Sum32())%len(enabled)]
}

// playbackResolve 播放取直链（小号模式）：
// main pickcode → 台账 rel_path → 小号 pickcode → 小号 Cookie 取直链。
// 返回 (直链, headers, 错误)；未启用/映射未命中返回 err=playbackSkipErr
var errPlaybackSkip = fmt.Errorf("playback: 未命中小号映射，回退主号")

func playbackResolve(db *gorm.DB, cfg *config.Config, mainPickcode, ua string) (string, map[string]string, error) {
	pcfg := loadPlaybackCfg()
	alt := playbackRouteForUA(pcfg, ua)
	if alt == nil {
		return "", nil, errPlaybackSkip
	}
	var sf model.SyncedFile
	if err := db.Where("pick_code = ? AND kind = ?", mainPickcode, "video").First(&sf).Error; err != nil || sf.RelPath == "" {
		return "", nil, errPlaybackSkip
	}
	var m model.PlaybackAltFile
	if err := db.Where("account_id = ? AND rel_path = ?", alt.ID, strings.Trim(sf.RelPath, "/")).First(&m).Error; err != nil || m.AltPickCode == "" {
		// 小号还没这个文件：记录待同步，回退主号
		return "", nil, errPlaybackSkip
	}
	u, hdrs, err := get115DownloadURL(m.AltPickCode, alt.Cookie, ua)
	if err != nil {
		log.Printf("[播放账号] ○ 小号「%s」取直链失败，回退主号: %v", alt.Name, err)
		return "", nil, errPlaybackSkip
	}
	// 设备登记（多端播放页展示）
	playbackTouchDevice(ua, alt.ID, alt.Name)
	return u, hdrs, nil
}

// playbackTouchDevice 设备登记/更新（多端播放 tab 展示设备→小号绑定）
func playbackTouchDevice(ua string, altID int64, altName string) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ua))
	uah := fmt.Sprint(h.Sum32())
	var d model.PlaybackDevice
	if err := model.DB.Where("ua_hash = ?", uah).First(&d).Error; err != nil {
		d = model.PlaybackDevice{UAHash: uah, UA: truncateStr(ua, 200), FirstSeen: time.Now()}
	}
	d.AltID = altID
	d.AltName = altName
	d.LastSeen = time.Now()
	model.DB.Save(&d)
}

// ---- 多端播放：设备列表 ----

// PlaybackDevices GET /playback/devices → 已知设备与小号绑定
func (h *Handler) PlaybackDevices(c *gin.Context) {
	var devices []model.PlaybackDevice
	model.DB.Order("last_seen DESC").Limit(50).Find(&devices)
	c.JSON(http.StatusOK, gin.H{"data": devices})
}

// relDirOf 取路径的目录部分
func relDirOf(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

// altReceiveShare 小号侧转存：snap 拿 fid → receive 进目标目录
// （与 share.go 的 receive 链路同协议，但用小号 Cookie 且不触发整理）
func altReceiveShare(altCookie, shareCode, receiveCode, targetCid string) error {
	var fids []string
	for offset := 0; ; offset += 1150 {
		body, err := getShareAPI("/share/snap", url.Values{
			"share_code":   {shareCode},
			"receive_code": {receiveCode},
			"cid":          {"0"},
			"offset":       {fmt.Sprint(offset)},
			"limit":        {"1150"},
			"asc":          {"1"},
			"fc_mix":       {"0"},
		}, altCookie, 15*time.Second)
		if err != nil {
			return fmt.Errorf("分享列表获取失败: %s", err.Error())
		}
		var snap struct {
			State bool `json:"state"`
			Data  struct {
				List []struct {
					Fid string `json:"fid"`
				} `json:"list"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &snap) != nil || !snap.State {
			return fmt.Errorf("分享列表被拒: %s", truncateStr(string(body), 100))
		}
		for _, it := range snap.Data.List {
			fids = append(fids, it.Fid)
		}
		if len(snap.Data.List) < 1150 {
			break
		}
	}
	if len(fids) == 0 {
		return nil
	}
	body, err := getShareAPI("/share/receive", url.Values{
		"share_code":   {shareCode},
		"receive_code": {receiveCode},
		"file_id":      {strings.Join(fids, ",")},
		"cid":          {targetCid},
	}, altCookie, 30*time.Second)
	if err != nil {
		return fmt.Errorf("转存提交失败: %s", err.Error())
	}
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return fmt.Errorf("转存被拒: %s", truncateStr(string(body), 120))
	}
	return nil
}
