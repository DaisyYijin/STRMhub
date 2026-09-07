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
//	→ 生成本地 STRM（writeStrmCd2）
//
// 媒体库根（RootPath）内的删除/改名事件同步增删本地 STRM。
// 识别失败的内容留在监控目录原地（不移动），等下次事件或手动整理重试。

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
	return gin.H{
		"running": w.running, "last_err": lastErr,
		"last_event": w.lastEvt.Format("01-02 15:04:05"), "organized": w.organized,
	}
}

var cd2Watch = &cd2WatchState{}

// StartCd2Watcher 后台常驻：每 10s 检查配置，开启则维持推送流（断线 5s 重连）。
// main.go 启动时挂 goroutine
func StartCd2Watcher(db *gorm.DB, cfg *config.Config) {
	go func() {
		h := &Handler{DB: db, Config: cfg}
		var cancel context.CancelFunc
		for {
			time.Sleep(10 * time.Second)
			c := h.loadCd2Cfg()
			if !c.OrgEnabled || c.OrgPending == "" || c.RootPath == "" || c.Endpoint == "" {
				if cancel != nil {
					cancel()
					cancel = nil
					cd2Watch.setRunning(false)
					log.Printf("[CD2监控] ○ 已停止（配置关闭或未完成）")
				}
				continue
			}
			if cancel != nil {
				continue // 已在运行
			}
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			cd2Watch.setRunning(true)
			log.Printf("[CD2监控] ▶ 实时监控启动：监控 %s → 整理到 %s", c.OrgPending, c.RootPath)
			go func(ctx context.Context, h *Handler) {
				for {
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
			return
		}
		// 直接落进媒体库根的新文件（不经监控目录，如手动归位）：补 STRM 镜像
		if cd2HasPrefix(ch.Path, libRoot) && !ch.IsDir && isVideoName(path.Base(ch.Path)) {
			if h.cd2MirrorStrm(cfg, ch.Path) == nil {
				log.Printf("[CD2监控] ▷ 库内新增，补 STRM: %s", truncateStr(ch.Path, 70))
			}
		}
	}
}

// cd2MirrorStrm 为库内文件补写 STRM（已存在则跳过）
func (h *Handler) cd2MirrorStrm(cfg cd2Cfg, fullPath string) error {
	relDir, name := cd2RelStrm(cfg.RootPath, fullPath)
	if name == "" || !isVideoName(name) {
		return fmt.Errorf("非视频或路径无效")
	}
	domain, format, keepExt, skipExist := h.getStrmConfig()
	if domain == "" {
		domain = "http://127.0.0.1:" + h.Config.ProxyPortStr()
	}
	_, err := writeStrmCd2(cfg.LocalPath, domain, format, keepExt, skipExist, relDir, name, fullPath)
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

// ==================== 整理引擎 ====================

var cd2OrgMu sync.Mutex

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

// cd2OrganizeAll 遍历监控目录顶层：目录 → 整目录单元；散视频 → 单文件单元
func (h *Handler) cd2OrganizeAll() int {
	cl, err := h.cd2Client()
	if err != nil {
		log.Printf("[CD2整理] ✗ %v", err)
		return 0
	}
	cfg := h.loadCd2Cfg()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	entries, err := cl.ListDir(ctx, cd2NormPath(cfg.OrgPending))
	cancel()
	if err != nil {
		log.Printf("[CD2整理] ✗ 列监控目录失败: %v", err)
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir {
			h.cd2OrganizeUnit(e.Path, "")
			n++
		} else if isVideoName(e.Name) {
			h.cd2OrganizeUnit(path.Dir(e.Path), e.Name)
			n++
		}
		time.Sleep(300 * time.Millisecond)
	}
	return n
}

// cd2OrganizeUnit 整理一个单元：unitDir 下全部文件（focusFile 非空时只处理
// 该文件与其同名附件）。识别 → 重命名 → 分类 → 移动 → STRM；失败留在原地
func (h *Handler) cd2OrganizeUnit(unitDir, focusFile string) {
	cfg := h.loadCd2Cfg()
	if cfg.RootPath == "" || cfg.LocalPath == "" {
		return
	}
	unitDir = cd2NormPath(unitDir)
	cl, err := h.cd2Client()
	if err != nil {
		log.Printf("[CD2整理] ✗ %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	files, err := cl.ListDir(ctx, unitDir)
	if err != nil {
		log.Printf("[CD2整理] ✗ 列目录失败 %s: %v", truncateStr(unitDir, 60), err)
		return
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

	// ===== 逐视频：模板重命名 + 移动 + STRM =====
	domain, format, keepExt, _ := h.getStrmConfig()
	if domain == "" {
		domain = "http://127.0.0.1:" + h.Config.ProxyPortStr()
	}
	var movedRootDir, movedMediaDir string
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
		if err := cl.EnsureDir(ctx, dstDir); err != nil {
			log.Printf("[CD2整理] ✗ 创建目录失败: %v", err)
			return
		}
		newName := parts[len(parts)-1]
		src := cd2Join(unitDir, vf.Name)
		if newName != vf.Name {
			if err := cl.Rename(ctx, src, newName); err != nil {
				log.Printf("[CD2整理] ○ 改名失败 %s→%s（按原名移动）: %v", vf.Name, newName, err)
				newName = vf.Name
			} else {
				src = cd2Join(unitDir, newName)
			}
		}
		if err := cl.MoveFiles(ctx, []string{src}, dstDir); err != nil {
			log.Printf("[CD2整理] ✗ 移动失败 %s → %s: %v", src, dstDir, err)
			continue
		}
		finalPath := cd2Join(dstDir, newName)
		relDir := ""
		if r, _ := cd2RelStrm(cfg.RootPath, finalPath); r != "" {
			relDir = r
		}
		if _, err := writeStrmCd2(cfg.LocalPath, domain, format, keepExt, false, relDir, newName, finalPath); err != nil {
			log.Printf("[CD2整理] ○ STRM 写入失败 %s: %v", finalPath, err)
		}
		movedRootDir, movedMediaDir = rootRel, dstDir
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
			if err := cl.MoveFiles(ctx, []string{cd2Join(unitDir, mf.Name)}, movedRootDir); err != nil {
				log.Printf("[CD2整理] ○ 元数据跟随失败 %s: %v", mf.Name, err)
			}
		}
	}

	cd2Watch.bumpOrganized()
	log.Printf("[CD2整理] ✓ %s → %s（%s）", truncateStr(dirName, 50), truncateStr(movedMediaDir, 60), media.Title)
	NotifyMessage("✦ CD2 自动整理", fmt.Sprintf("%s\n→ %s", dirName, movedMediaDir))
}
