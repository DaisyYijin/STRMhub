package api

// CD2 实时监控整理：
//
// 订阅 CD2 的 PushMessage 文件系统变更流，监控目录（OrgPending）里出现新视频
// 时防抖合并（同目录文件视为同一部影视），然后走与 115 整理同源的策略链：
//
//	识别（AV 番号 → TMDB 文件名 → 目录名兜底 → MetaTube 标题）
//	→ 重命名模板（buildNewNameWithTemplate，含 Season 目录）
//	→ 二级分类（classifyMedia + mediaTypeCategory）
//	→ CD2 写操作（Rename 原地改名 → MoveFile 移到 媒体库根/分类/片目目录）
//	→ 生成本地 STRM（writeStrmCd2，带 HMAC 签名）
//
// 媒体库根（RootPath）内的删除/改名事件同步增删本地 STRM；库内新增只镜像
// STRM、绝不整理（防"监控目录包含媒体库"的误配置自我消化库内容）。
// 识别失败的内容留在监控目录原地（不移动），等下次事件或手动整理重试。
// 整理单元全局串行（cd2OrgMu），事件驱动与手动整理不会交叉执行。

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strmhub/internal/cd2"
	"strmhub/internal/config"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 监控器状态 ====================

type cd2WatchState struct {
	mu        sync.Mutex
	running   bool
	lastErr   string
	lastEvt   time.Time
	organized int
}

func (w *cd2WatchState) setRunning(v bool) {
	w.mu.Lock()
	w.running = v
	w.mu.Unlock()
}

func (w *cd2WatchState) setErr(e string) {
	w.mu.Lock()
	w.lastErr = e
	w.mu.Unlock()
}

func (w *cd2WatchState) touchEvt() {
	w.mu.Lock()
	w.lastEvt = time.Now()
	w.mu.Unlock()
}

func (w *cd2WatchState) bumpOrganized() {
	w.mu.Lock()
	w.organized++
	w.mu.Unlock()
}

func (w *cd2WatchState) snapshot() gin.H {
	w.mu.Lock()
	defer w.mu.Unlock()
	lastErr := w.lastErr
	if w.running {
		lastErr = ""
	}
	lastEvt := ""
	if !w.lastEvt.IsZero() {
		lastEvt = w.lastEvt.Format("01-02 15:04:05")
	}
	return gin.H{
		"running": w.running, "last_err": lastErr,
		"last_event": lastEvt, "organized": w.organized,
	}
}

var cd2Watch = &cd2WatchState{}

// StartCd2Watcher 后台常驻：每 10s 检查配置，开启则维持推送流（断线 5s 重连）。
// 配置指纹变化（地址/账号/目录/开关）即刻重启流，不等旧连接自然断开。
// main.go 启动时挂 goroutine
func StartCd2Watcher(db *gorm.DB, cfg *config.Config) {
	go func() {
		h := &Handler{DB: db, Config: cfg}
		var cancel context.CancelFunc
		var runKey string
		for {
			time.Sleep(10 * time.Second)
			c := h.loadCd2Cfg()
			key := fmt.Sprintf("%s|%s|%s|%s|%s|%v",
				c.Endpoint, c.Username, c.Password, c.OrgPending, c.RootPath, c.OrgEnabled)
			on := c.OrgEnabled && c.OrgPending != "" && c.RootPath != "" && c.Endpoint != ""
			if !on {
				if cancel != nil {
					cancel()
					cancel = nil
					runKey = ""
					cd2Watch.setRunning(false)
					log.Printf("[CD2监控] ○ 已停止（配置关闭或未完成）")
				}
				continue
			}
			if cancel != nil {
				if key == runKey {
					continue // 配置未变，流继续
				}
				cancel()
				cancel = nil
				cd2Watch.setRunning(false)
				log.Printf("[CD2监控] ↻ 配置变更，重启监控流")
			}
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			runKey = key
			cd2Watch.setRunning(true)
			log.Printf("[CD2监控] ▶ 实时监控启动：监控 %s → 整理到 %s", c.OrgPending, c.RootPath)
			go func(ctx context.Context, h *Handler) {
				for {
					cd2MaybeBackfill(h)
					cl, err := h.cd2Client()
					if err != nil {
						cd2Watch.setErr(err.Error())
						select {
						case <-ctx.Done():
							return
						case <-time.After(30 * time.Second):
							continue
						}
					}
					werr := cl.WatchOnce(ctx, func(ch cd2.Change) { h.cd2HandleChange(ch) })
					if ctx.Err() != nil {
						return
					}
					if werr != nil {
						cd2Watch.setErr(werr.Error())
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Second): // 断线重连间隔
					}
				}
			}(ctx, h)
		}
	}()
}

// cd2MaybeBackfill 停机/断线期间的事件已丢失，靠周期性全量整理兜底：
// 流（重）建立前补一次，此后至多每 10 分钟一次；手动整理进行中则让路
var (
	cd2BackfillMu   sync.Mutex
	cd2LastBackfill time.Time
)

func cd2MaybeBackfill(h *Handler) {
	cd2BackfillMu.Lock()
	if time.Since(cd2LastBackfill) < 10*time.Minute {
		cd2BackfillMu.Unlock()
		return
	}
	cd2LastBackfill = time.Now()
	cd2BackfillMu.Unlock()

	if !cd2OrgMu.TryLock() {
		return // 手动整理在跑，无需补
	}
	defer cd2OrgMu.Unlock()
	n := h.cd2OrganizeAll()
	if n > 0 {
		log.Printf("[CD2监控] ▷ 补漏整理 %d 个单元", n)
	}
}

// ==================== 路径工具（可测试） ====================

// cd2NormPath 规范化 CD2 绝对路径：以 / 开头、去尾部 /
func cd2NormPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "/"
	}
	return "/" + p
}

// cd2HasPrefix 判断 CD2 路径 p 是否位于目录 root 下（含 root 自身）
func cd2HasPrefix(p, root string) bool {
	p, root = cd2NormPath(p), cd2NormPath(root)
	if root == "/" {
		return true
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// cd2Join 拼接 CD2 绝对路径（跳过空段）
func cd2Join(segs ...string) string {
	var parts []string
	for _, s := range segs {
		s = strings.Trim(s, "/")
		if s != "" {
			parts = append(parts, s)
		}
	}
	return "/" + strings.Join(parts, "/")
}

// cd2RelStrm 本地 STRM 相对路径：fullPath 相对 root 的位置，
// 返回 relDir（"/"分隔，无前后斜杠）与文件名
func cd2RelStrm(root, fullPath string) (relDir, name string) {
	root, fullPath = cd2NormPath(root), cd2NormPath(fullPath)
	rel := strings.TrimPrefix(fullPath, root)
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return "", ""
	}
	dir, f := path.Split(rel)
	return strings.Trim(dir, "/"), f
}

// cd2LocalStrmPath fullPath 对应的本地 STRM 文件绝对路径（与 writeStrmCd2 同构）
func cd2LocalStrmPath(localRoot, root, fullPath string) string {
	relDir, name := cd2RelStrm(root, fullPath)
	if name == "" {
		return ""
	}
	return filepath.Join(localRoot, filepath.FromSlash(relDir), name+".strm")
}

// ==================== 事件处理 ====================

// 防抖表：监控目录下同一父目录的新文件合并为一个整理单元，
// 最后一个文件到达后静默 8s 才动手（网盘批量转存时文件陆续到位）
const cd2DebounceDelay = 8 * time.Second

var (
	cd2DebounceMu sync.Mutex
	cd2Debounce   = map[string]*time.Timer{}
)

func (h *Handler) cd2HandleChange(ch cd2.Change) {
	cd2Watch.touchEvt()
	cfg := h.loadCd2Cfg()
	libRoot := cd2NormPath(cfg.RootPath)
	pendRoot := cd2NormPath(cfg.OrgPending)

	switch ch.Type {
	case "delete":
		if cd2HasPrefix(ch.Path, libRoot) {
			h.cd2RemoveStrm(cfg, ch.Path)
			log.Printf("[CD2监控] ✂ 库内删除，移除 STRM: %s", truncateStr(ch.Path, 70))
		}
	case "rename":
		if cd2HasPrefix(ch.Path, libRoot) {
			h.cd2RemoveStrm(cfg, ch.Path)
		}
		if ch.NewPath != "" && cd2HasPrefix(ch.NewPath, libRoot) && !ch.IsDir {
			if h.cd2MirrorStrm(cfg, ch.NewPath) == nil {
				log.Printf("[CD2监控] ✎ 库内改名，更新 STRM: %s", truncateStr(ch.NewPath, 70))
			}
		}
	case "create":
		// 媒体库子树优先判定：只镜像 STRM，绝不整理——防"监控目录包含
		// 媒体库"的误配置把库内容再整理一遍
		if cd2HasPrefix(ch.Path, libRoot) {
			if !ch.IsDir && isVideoName(path.Base(ch.Path)) && h.cd2MirrorStrm(cfg, ch.Path) == nil {
				log.Printf("[CD2监控] ▷ 库内新增，补 STRM: %s", truncateStr(ch.Path, 70))
			}
			return
		}
		if cd2HasPrefix(ch.Path, pendRoot) && !ch.IsDir {
			dir := path.Dir(cd2NormPath(ch.Path))
			cd2DebounceMu.Lock()
			if t, ok := cd2Debounce[dir]; ok {
				t.Reset(cd2DebounceDelay)
			} else {
				cd2Debounce[dir] = time.AfterFunc(cd2DebounceDelay, func() {
					cd2DebounceMu.Lock()
					delete(cd2Debounce, dir)
					cd2DebounceMu.Unlock()
					h.cd2OrganizeUnit(dir, "")
				})
			}
			cd2DebounceMu.Unlock()
		}
	}
}

// cd2MirrorStrm 为库内文件补写 STRM（内容一致则跳过）
func (h *Handler) cd2MirrorStrm(cfg cd2Cfg, fullPath string) error {
	relDir, name := cd2RelStrm(cfg.RootPath, fullPath)
	if name == "" || !isVideoName(name) {
		return fmt.Errorf("非视频或路径无效")
	}
	domain, format, keepExt, skipExist := h.getStrmConfig()
	if domain == "" {
		domain = "http://127.0.0.1:" + h.Config.ProxyPortStr()
	}
	_, err := writeStrmCd2(cfg.LocalPath, domain, format, keepExt, skipExist, relDir, name, h.cd2MakeID(fullPath))
	return err
}

// cd2RemoveStrm 删除库内文件对应的本地 STRM
func (h *Handler) cd2RemoveStrm(cfg cd2Cfg, fullPath string) {
	p := cd2LocalStrmPath(cfg.LocalPath, cfg.RootPath, fullPath)
	if p == "" {
		return
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		log.Printf("[CD2监控] ○ 移除 STRM 失败 %s: %v", p, err)
	}
}

// ==================== 通知聚合 ====================
// 批量入库时逐单元推送会刷屏：10 秒窗口内的单元聚合成一条
// （手动整理不进聚合，有自己的汇总通知）

var (
	cd2NotifyMu    sync.Mutex
	cd2NotifyItems []string
	cd2NotifyCount int
	cd2NotifyTimer *time.Timer
)

const (
	cd2NotifyDelay = 10 * time.Second
	cd2NotifyShow  = 8
)

func cd2NotifySchedule(title, target string) {
	cd2NotifyMu.Lock()
	defer cd2NotifyMu.Unlock()
	cd2NotifyCount++
	if len(cd2NotifyItems) < cd2NotifyShow {
		cd2NotifyItems = append(cd2NotifyItems, title+"\n→ "+target)
	}
	if cd2NotifyTimer != nil {
		cd2NotifyTimer.Reset(cd2NotifyDelay)
		return
	}
	cd2NotifyTimer = time.AfterFunc(cd2NotifyDelay, func() {
		cd2NotifyMu.Lock()
		items := append([]string(nil), cd2NotifyItems...)
		count := cd2NotifyCount
		cd2NotifyItems, cd2NotifyCount, cd2NotifyTimer = nil, 0, nil
		cd2NotifyMu.Unlock()
		if count == 0 {
			return
		}
		body := strings.Join(items, "\n")
		if count > cd2NotifyShow {
			body += fmt.Sprintf("\n…（共 %d 个单元）", count)
		}
		NotifyMessage("✦ CD2 自动整理", fmt.Sprintf("入库 %d 个单元：\n%s", count, body))
	})
}

// ==================== 整理引擎 ====================

var cd2OrgMu sync.Mutex

// cd2MovePlan 一个视频的落位计划（整理引擎内部传递）
type cd2MovePlan struct {
	vf      cd2.File
	newName string
	rootRel string // 片目根（相对媒体库）
	dstDir  string // 实际落位目录（剧集含 Season 层）
}

// Cd2OrgStatus GET /cd2/org/status：监控运行状态
func (h *Handler) Cd2OrgStatus(c *gin.Context) {
	cfg := h.loadCd2Cfg()
	st := cd2Watch.snapshot()
	st["enabled"] = cfg.OrgEnabled
	st["pending"] = cfg.OrgPending
	c.JSON(http.StatusOK, gin.H{"data": st})
}

// Cd2OrgRun POST /cd2/org/run：手动全量整理监控目录一遍
// （事件驱动之外的兜底/重试入口）
func (h *Handler) Cd2OrgRun(c *gin.Context) {
	cfg := h.loadCd2Cfg()
	if cfg.OrgPending == "" || cfg.RootPath == "" || cfg.Endpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先配置监控目录与媒体库根"})
		return
	}
	if !cd2OrgMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "CD2 整理已在进行中"})
		return
	}
	go func() {
		defer cd2OrgMu.Unlock()
		n := h.cd2OrganizeAll()
		log.Printf("[CD2整理] 手动整理完成：%d 个单元", n)
		NotifyMessage("▤ CD2 手动整理完成", fmt.Sprintf("处理整理单元：%d 个", n))
	}()
	c.JSON(http.StatusOK, gin.H{"message": "整理已开始，结果看日志与通知"})
}

// cd2OrganizeAll 遍历监控目录顶层：目录 → 整目录单元；散视频 → 单文件单元。
// 调用方须持有 cd2OrgMu（手动整理 / 回漏整理）
func (h *Handler) cd2OrganizeAll() int {
	cl, err := h.cd2Client()
	if err != nil {
		log.Printf("[CD2整理] ✗ %v", err)
		return 0
	}
	cfg := h.loadCd2Cfg()
	libRoot := cd2NormPath(cfg.RootPath)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	entries, err := cl.ListDir(ctx, cd2NormPath(cfg.OrgPending))
	cancel()
	if err != nil {
		log.Printf("[CD2整理] ✗ 列监控目录失败: %v", err)
		return 0
	}
	n := 0
	for _, e := range entries {
		if cd2HasPrefix(e.Path, libRoot) {
			continue // 监控目录嵌套媒体库：库内条目绝不整理
		}
		if e.IsDir {
			h.cd2OrganizeUnitCore(e.Path, "", false)
			n++
		} else if isVideoName(e.Name) {
			h.cd2OrganizeUnitCore(path.Dir(e.Path), e.Name, false)
			n++
		}
		time.Sleep(300 * time.Millisecond)
	}
	return n
}

// cd2OrganizeUnit 事件驱动的整理入口：全局串行（与手动整理互斥），
// 阻塞排队直到轮到自己
func (h *Handler) cd2OrganizeUnit(unitDir, focusFile string) {
	cd2OrgMu.Lock()
	defer cd2OrgMu.Unlock()
	h.cd2OrganizeUnitCore(unitDir, focusFile, true)
}

// cd2OrganizeUnitCore 整理一个单元：unitDir 下全部文件（focusFile 非空时只处理
// 该文件与其同名附件）。识别 → 重命名 → 分类 → 移动 → STRM；失败留在原地。
// 调用方须持有 cd2OrgMu；notify=true 时进聚合并入通知
func (h *Handler) cd2OrganizeUnitCore(unitDir, focusFile string, notify bool) {
	cfg := h.loadCd2Cfg()
	if cfg.RootPath == "" || cfg.LocalPath == "" {
		return
	}
	unitDir = cd2NormPath(unitDir)
	if cd2HasPrefix(unitDir, cd2NormPath(cfg.RootPath)) {
		return // 媒体库子树防护（防误配置）
	}
	cl, err := h.cd2Client()
	if err != nil {
		log.Printf("[CD2整理] ✗ %v", err)
		return
	}
	// 大包（百集剧集逐集改名+移动）耗时可观，给足窗口
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	files, err := cl.ListDir(ctx, unitDir)
	if err != nil {
		log.Printf("[CD2整理] ✗ 列目录失败 %s: %v", truncateStr(unitDir, 60), err)
		return
	}

	// 广告/超小视频过滤：沿用 115 整理的最小体积设置
	minBytes := int64(0)
	if oc, oerr := h.loadOrgConfig(); oerr == nil && oc.MinSize > 0 {
		minBytes = oc.MinSize * 1024 * 1024
	}
	focusBase := ""
	if focusFile != "" {
		focusBase = strings.TrimSuffix(focusFile, pathExt(focusFile))
	}
	var videos, subs, metas []cd2.File
	for _, f := range files {
		if f.IsDir {
			continue
		}
		if focusFile != "" && f.Name != focusFile &&
			!strings.HasPrefix(f.Name, focusBase+".") && !strings.HasPrefix(f.Name, focusBase+" ") {
			continue // 只跟随同基名附件（字幕/NFO/封面）
		}
		switch classifyFile(f.Name) {
		case FileTypeVideo:
			if isAdOnlyVideo(f.Name) || (minBytes > 0 && f.Size > 0 && f.Size < minBytes) {
				continue // 广告/引流与超小视频不随正片入库，留在原地
			}
			videos = append(videos, f)
		case FileTypeSubtitle:
			subs = append(subs, f)
		case FileTypeNFO, FileTypeStdImage:
			metas = append(metas, f)
		}
	}
	if len(videos) == 0 {
		return
	}
	mainVideo := videos[0]
	for _, v := range videos {
		if v.Size > mainVideo.Size {
			mainVideo = v
		}
	}

	dirName := path.Base(unitDir)
	replaceRules := loadReplaceRules()
	ensureRenameTpl()
	name := mainVideo.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}

	log.Printf("[CD2整理] ▶ 整理 %s（样本: %s）", truncateStr(dirName, 50), truncateStr(mainVideo.Name, 50))

	// ===== 识别（与 115 引擎同源的顺序：AV 番号 → 文件名 → 目录名 → MetaTube） =====
	var media *TmdbMedia
	mainParsed := parseFileName(name)
	if avNum := detectAVNumber(dirName, mainVideo.Name); avNum != "" {
		media = &TmdbMedia{Title: avNum, MediaType: "av"}
	} else {
		tc, err := loadTmdbClient()
		if err != nil {
			log.Printf("[CD2整理] ○ TMDB 不可用（%v），%s 留在监控目录下轮重试", err, dirName)
			return
		}
		useDirName := false
		if mainParsed.Title == "" || isEpisodeOnly(mainParsed.Title) {
			dirParsed := parseFileName(dirName)
			if dirParsed.Title != "" && !isEpisodeOnly(dirParsed.Title) {
				if dirParsed.Season == 0 {
					dirParsed.Season = mainParsed.Season
				}
				if dirParsed.Episode == 0 {
					dirParsed.Episode = mainParsed.Episode
				}
				mainParsed = dirParsed
				useDirName = true
			}
		}
		if mainParsed.Title == "" {
			log.Printf("[CD2整理] ○ %s 无法提取标题，留在监控目录", truncateStr(dirName, 50))
			return
		}
		media, err = tc.recognize(mainParsed)
		if (err != nil || media == nil) && !useDirName {
			dirParsed := parseFileName(dirName)
			if dirParsed.Title != "" && !isEpisodeOnly(dirParsed.Title) {
				if dirParsed.Season == 0 {
					dirParsed.Season = mainParsed.Season
				}
				if dirParsed.Episode == 0 {
					dirParsed.Episode = mainParsed.Episode
				}
				media, err = tc.recognize(dirParsed)
			}
		}
		if err != nil {
			log.Printf("[CD2整理] ○ TMDB 暂时不可达（%v），%s 留在监控目录下轮重试", err, dirName)
			return
		}
		if media == nil {
			// 无番号 AV 标题兜底（MetaTube）
			if avMedia, num := metatubeSearchTitle(dirName); avMedia != nil && num != "" {
				media = &TmdbMedia{Title: num, MediaType: "av"}
			} else {
				log.Printf("[CD2整理] ○ %s 未识别到影视信息，留在监控目录", truncateStr(dirName, 50))
				return
			}
		}
	}

	category := classifyMedia(media)
	typeRoot := mediaTypeCategory(media.MediaType)

	// 主视频排首位（洗版判定/整理记录以它为准）
	for i, v := range videos {
		if v.Name == mainVideo.Name && i != 0 {
			videos[0], videos[i] = videos[i], videos[0]
			break
		}
	}

	// ===== 落位计划：逐视频算模板新名与目标目录 =====
	plans := make([]cd2MovePlan, 0, len(videos))
	for _, vf := range videos {
		fp := parseFileName(vf.Name)
		if fp.Season == 0 {
			fp.Season = mainParsed.Season
		}
		newRel := buildNewNameWithTemplate(media, fp, vf.Name)
		if newRel == "" {
			newRel = vf.Name
		}
		parts := strings.Split(newRel, "/")
		rootRel := cd2Join(typeRoot, category, parts[0])
		dstDir := rootRel
		if media.MediaType == "tv" && len(parts) >= 2 { // Season 层
			dstDir = cd2Join(rootRel, parts[1])
		}
		plans = append(plans, cd2MovePlan{vf: vf, newName: parts[len(parts)-1], rootRel: rootRel, dstDir: dstDir})
	}

	// ===== 去重/洗版开关：需配置「已存在」目录且不在媒体库子树内 =====
	existingRoot := cd2NormPath(cfg.OrgExisting)
	washOn := existingRoot != "/" && !cd2HasPrefix(existingRoot, cd2NormPath(cfg.RootPath))
	if cfg.OrgExisting != "" && !washOn {
		log.Printf("[CD2整理] ○ 已存在目录在媒体库内，去重/洗版停用: %s", existingRoot)
	}
	if washOn {
		switch h.cd2TryWash(ctx, cl, cfg, media, category, plans[0], existingRoot) {
		case washNotBetter:
			// 库内已有更优版本：整个单元（含附件）转已存在/<单元名>/
			dest := cd2Join(existingRoot, path.Base(unitDir))
			diverted := 0
			for _, p := range plans {
				if h.cd2DivertExisting(ctx, cl, dest, cd2Join(unitDir, p.vf.Name), p.vf.Name) {
					diverted++
				}
			}
			for _, sf := range subs {
				if h.cd2DivertExisting(ctx, cl, dest, cd2Join(unitDir, sf.Name), sf.Name) {
					diverted++
				}
			}
			for _, mf := range metas {
				if h.cd2DivertExisting(ctx, cl, dest, cd2Join(unitDir, mf.Name), mf.Name) {
					diverted++
				}
			}
			log.Printf("[CD2整理] ○ 《%s》洗版判定：库内版本更优，%d 个文件 → %s", media.Title, diverted, dest)
			cd2Watch.bumpOrganized()
			if notify {
				cd2NotifySchedule(dirName, "已存在（库内版本更优）")
			}
			return
			// washReplaced：旧版已让位，继续正常入库
		}
	}

	// ===== 逐视频：去重 → 改名 → 移动 → STRM =====
	domain, format, keepExt, _ := h.getStrmConfig()
	if domain == "" {
		domain = "http://127.0.0.1:" + h.Config.ProxyPortStr()
	}
	dstCache := map[string][]cd2.File{} // 目标目录现有文件（单元内复用；含本单元刚入库的）
	var movedRootDir, movedMediaDir string
	recordName := "" // 主视频最终落位的文件名（整理记录用）
	for _, p := range plans {
		if washOn {
			files, ok := dstCache[p.dstDir]
			if !ok {
				files, err = cl.ListDir(ctx, p.dstDir)
				if err != nil {
					files = nil // 目录尚不存在/查询失败：跳过去重，移动照常
				}
				dstCache[p.dstDir] = files
			}
			if cd2DupExists(files, p.vf, p.newName) {
				if h.cd2DivertExisting(ctx, cl, existingRoot, cd2Join(unitDir, p.vf.Name), p.vf.Name) {
					log.Printf("[CD2整理] ○ 去重：%s 与库内文件相同（SHA1/同名同大小），转已存在", p.vf.Name)
				}
				continue
			}
		}
		if err := cl.EnsureDir(ctx, p.dstDir); err != nil {
			log.Printf("[CD2整理] ✗ 创建目录失败: %v", err)
			return
		}
		newName := p.newName
		src := cd2Join(unitDir, p.vf.Name)
		if newName != p.vf.Name {
			if err := cl.Rename(ctx, src, newName); err != nil {
				log.Printf("[CD2整理] ○ 改名失败 %s→%s（按原名移动）: %v", p.vf.Name, newName, err)
				newName = p.vf.Name
			} else {
				src = cd2Join(unitDir, newName)
			}
		}
		if err := cl.MoveFiles(ctx, []string{src}, p.dstDir); err != nil {
			log.Printf("[CD2整理] ✗ 移动失败 %s → %s: %v", src, p.dstDir, err)
			continue
		}
		finalPath := cd2Join(p.dstDir, newName)
		relDir, _ := cd2RelStrm(cfg.RootPath, finalPath)
		if _, err := writeStrmCd2(cfg.LocalPath, domain, format, keepExt, false, relDir, newName, h.cd2MakeID(finalPath)); err != nil {
			log.Printf("[CD2整理] ○ STRM 写入失败 %s: %v", finalPath, err)
		}
		// 入库后补进目标缓存：同单元内后续重复集也能被拦下
		dstCache[p.dstDir] = append(dstCache[p.dstDir], cd2.File{Name: newName, Size: p.vf.Size, Sha1: p.vf.Sha1})
		movedRootDir, movedMediaDir = p.rootRel, p.dstDir
		if recordName == "" {
			recordName = newName
		}
	}

	// ===== 附件跟随：字幕 → 视频所在目录；NFO/封面 → 片目根目录 =====
	if movedMediaDir != "" {
		for _, sf := range subs {
			if err := cl.MoveFiles(ctx, []string{cd2Join(unitDir, sf.Name)}, movedMediaDir); err != nil {
				log.Printf("[CD2整理] ○ 字幕跟随失败 %s: %v", sf.Name, err)
			}
		}
	}
	if movedRootDir != "" {
		for _, mf := range metas {
			mvName := mf.Name
			// 海报.png/封面.jpg → poster.ext：Emby 只认标准名
			// （与 115 整理同规则）
			if base := strings.ToLower(baseName(mvName)); base == "海报" || base == "封面" {
				newName := "poster" + pathExt(mvName)
				if err := cl.Rename(ctx, cd2Join(unitDir, mvName), newName); err == nil {
					log.Printf("[CD2整理] ✓ 海报 %s → %s", mvName, newName)
					mvName = newName
				}
			}
			if err := cl.MoveFiles(ctx, []string{cd2Join(unitDir, mvName)}, movedRootDir); err != nil {
				log.Printf("[CD2整理] ○ 元数据跟随失败 %s: %v", mf.Name, err)
			}
		}
	}

	cd2Watch.bumpOrganized()
	log.Printf("[CD2整理] ✓ %s → %s（%s）", truncateStr(dirName, 50), truncateStr(movedMediaDir, 60), media.Title)
	if notify {
		cd2NotifySchedule(dirName, movedMediaDir)
	}
	// 整理记录（CD2 洗版判定用；路径相对媒体库根，与 115 记录按来源隔离）
	if movedMediaDir != "" && recordName != "" {
		relTarget := strings.Trim(strings.TrimPrefix(movedMediaDir, cd2NormPath(cfg.RootPath)), "/")
		recordMedia(media, category, relTarget+"/"+recordName, "cd2")
	}
}

// cd2DupExists 去重判定：目标目录现有文件中已有同 SHA1 文件，
// 或整理后同名同大小文件（网盘不提供 SHA1 时的兜底）
func cd2DupExists(existing []cd2.File, f cd2.File, newName string) bool {
	for _, e := range existing {
		if f.Sha1 != "" && e.Sha1 != "" && strings.EqualFold(e.Sha1, f.Sha1) {
			return true
		}
		if e.Name == newName && e.Size > 0 && e.Size == f.Size {
			return true
		}
	}
	return false
}

// cd2DivertExisting 把监控目录里的文件转进指定目录（已存在及其子目录）；
// 返回是否成功。失败时文件留在原地等下轮
func (h *Handler) cd2DivertExisting(ctx context.Context, cl *cd2.Client, dest, src, label string) bool {
	if err := cl.EnsureDir(ctx, dest); err != nil {
		log.Printf("[CD2整理] ✗ 已存在目录不可用（%s）: %v", truncateStr(dest, 60), err)
		return false
	}
	if err := cl.MoveFiles(ctx, []string{src}, dest); err != nil {
		log.Printf("[CD2整理] ○ 转移失败 %s → %s: %v", label, truncateStr(dest, 60), err)
		return false
	}
	return true
}

// cd2TryWash 洗版判定（与 115 的 tryWashReplace 同语义；比较对象来自 CD2
// 实时目录列表而非本地台账，天然新鲜）。返回 washReplaced/washNotBetter/washSkip：
//   - replaced：旧版已迁「已存在/洗版-旧版本/<片目[/Season]>」，本地 STRM 已清，
//     调用方继续正常入库
//   - notbetter：库内更优，调用方应把新单元转已存在
//   - skip：未配置策略/无整理记录/库内无文件/新集/coexist 等，正常入库
func (h *Handler) cd2TryWash(ctx context.Context, cl *cd2.Client, cfg cd2Cfg, media *TmdbMedia, category string, mainPlan cd2MovePlan, existingRoot string) string {
	rec, ok := lookupMediaRecordSrc(media, "cd2")
	if !ok {
		return washSkip
	}
	st := matchWashStrategy(media.MediaType, category)
	if st == nil || len(st.PriorityLevel) == 0 {
		return washSkip
	}
	mode := st.Mode
	if mode == "" {
		mode = "replace"
	}
	// 记录里的 TargetPath 是相对媒体库的文件路径；TV 的目录段即 Season 层
	targetDir := path.Dir(strings.Trim(rec.TargetPath, "/"))
	files, err := cl.ListDir(ctx, cd2Join(cfg.RootPath, targetDir))
	if err != nil || len(files) == 0 {
		return washSkip
	}
	var libNames []string
	var libVideos, libOthers []cd2.File
	for _, f := range files {
		if f.IsDir {
			continue
		}
		libNames = append(libNames, f.Name)
		if classifyFile(f.Name) == FileTypeVideo {
			libVideos = append(libVideos, f)
		} else {
			libOthers = append(libOthers, f)
		}
	}
	if len(libNames) == 0 {
		return washSkip
	}
	// 比较对象选取：剧集用同一集；电影取第一个视频
	oldName := ""
	if media.MediaType == "tv" {
		if newEp := parseFileName(mainPlan.newName).Episode; newEp > 0 {
			for _, ln := range libNames {
				if parseFileName(ln).Episode == newEp {
					oldName = ln
					break
				}
			}
		}
	}
	if oldName == "" {
		for _, ln := range libNames {
			if classifyFile(ln) == FileTypeVideo {
				oldName = ln
				break
			}
		}
	}
	if oldName == "" {
		oldName = libNames[0]
	}
	// 新集守卫：该集在库内从未出现 → 新增集正常入库，不做画质比较
	if media.MediaType == "tv" {
		if newEp := parseFileName(mainPlan.newName).Episode; newEp > 0 {
			if parseFileName(oldName).Episode != newEp {
				return washSkip
			}
		}
	}
	if mode == "coexist" {
		log.Printf("[CD2整理] ○ 洗版判定：coexist 模式，%s 与库内版本共存入库", truncateStr(mainPlan.newName, 60))
		return washSkip
	}
	if mode == "skip" {
		log.Printf("[CD2整理] ○ 洗版判定：skip 模式，库内已有 %s", truncateStr(mainPlan.newName, 60))
		return washNotBetter
	}
	if st.Scope == "group" {
		newPix := strings.ToLower(ParseResourceInfo(mainPlan.newName).Pix)
		oldPix := strings.ToLower(ParseResourceInfo(oldName).Pix)
		if newPix != oldPix {
			log.Printf("[CD2整理] ○ 洗版判定：group 模式新分辨率分组（%s vs 库内 %s），共存入库", newPix, oldPix)
			return washSkip
		}
	}
	if !washDecision(mainPlan.newName, []string{oldName}, st.PriorityLevel) {
		log.Printf("[CD2整理] ○ 《%s》洗版判定：库内版本更优（mode=%s）", media.Title, mode)
		return washNotBetter
	}
	// 新版更优：旧版迁出（group 只搬同分辨率组，附件不随迁；
	// 旧版去向配置统一映射到「已存在」，不做真删除）
	moveOut := append([]cd2.File(nil), libVideos...)
	if st.Scope == "group" {
		newPix := strings.ToLower(ParseResourceInfo(mainPlan.newName).Pix)
		filtered := moveOut[:0]
		for _, f := range moveOut {
			if strings.ToLower(ParseResourceInfo(f.Name).Pix) == newPix {
				filtered = append(filtered, f)
			}
		}
		moveOut = filtered
	} else {
		moveOut = append(moveOut, libOthers...)
	}
	destRel := path.Base(targetDir)
	if strings.HasPrefix(strings.ToLower(destRel), "season") {
		destRel = path.Base(path.Dir(targetDir)) + "/" + destRel
	}
	dest := cd2Join(existingRoot, "洗版-旧版本", destRel)
	if err := cl.EnsureDir(ctx, dest); err != nil {
		log.Printf("[CD2整理] ✗ 洗版：创建旧版目录失败: %v（本轮跳过，库保持原状）", err)
		return washSkip
	}
	if len(moveOut) > 0 {
		paths := make([]string, 0, len(moveOut))
		for _, f := range moveOut {
			paths = append(paths, f.Path)
		}
		if err := cl.MoveFiles(ctx, paths, dest); err != nil {
			log.Printf("[CD2整理] ✗ 洗版迁移旧版失败: %v（本轮跳过，库保持原状）", err)
			return washSkip
		}
		// 迁出的旧版清除本地 STRM（CD2 删除事件随后也会清，双保险幂等）
		for _, f := range moveOut {
			h.cd2RemoveStrm(cfg, f.Path)
		}
	}
	log.Printf("[CD2整理] ✦ 《%s》洗版：旧版 %d 个文件 → %s", media.Title, len(moveOut), truncateStr(dest, 60))
	return washReplaced
}
