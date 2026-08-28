package api

// ==================== 观影门户（默认 6688） ====================
//
// 独立于管理后台的公开门户：海报墙 + 分类浏览 + 网页直接播放。
// 播放走 6086 的 302 直链（浏览器能播的编码直出：MP4/H.264；
// MKV/H.265 视浏览器能力而定）。海报经本服务代理并落盘缓存，
// 局网客户端无需访问 TMDB。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

var portalCfg *config.Config

// StartPortal 启动观影门户（独立端口，公开访问）
func StartPortal(cfg *config.Config) {
	portalCfg = cfg
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/", portalPage)
	r.GET("/api/portal/nav", portalNav)
	r.GET("/api/portal/list", portalList)
	r.GET("/api/portal/detail", portalDetail)
	r.GET("/poster/*path", portalPoster)

	go portalBackfillWorker()
	addr := ":" + fmt.Sprint(cfg.PortalPort)
	log.Printf("观影门户启动: http://localhost:%d", cfg.PortalPort)
	if err := r.Run(addr); err != nil {
		log.Printf("门户启动失败（端口 %d 被占用？）: %v", cfg.PortalPort, err)
	}
}

// portalTitleEntry 从台账聚合出的一个标题条目
type portalTitleEntry struct {
	Key       string // 标题目录的相对路径（详情查询用）
	Title     string
	Year      string
	TmdbID    int
	MediaType string
	Category  string
	LastAt    time.Time
}

// portalScanLedger 全量扫描台账，按"标题目录"聚合（库名/电影|剧集/分类/标题目录/…）
func portalScanLedger() map[string]*portalTitleEntry {
	var sfs []model.SyncedFile
	model.DB.Where("kind = ?", "video").Find(&sfs)
	out := map[string]*portalTitleEntry{}
	for _, sf := range sfs {
		segs := strings.Split(strings.Trim(sf.RelPath, "/"), "/")
		if len(segs) < 3 {
			continue
		}
		var mediaType, category, titleDir string
		if len(segs) >= 4 {
			mediaType, category, titleDir = segs[1], segs[2], segs[3]
		} else {
			mediaType, category, titleDir = segs[0], "", segs[1]
		}
		if mediaType != "电影" && mediaType != "剧集" {
			continue
		}
		key := strings.Join(segs[:4], "/")
		e, ok := out[key]
		if !ok {
			title, year, tmdb := parseTitleDir(titleDir)
			e = &portalTitleEntry{
				Key: key, Title: title, Year: year, TmdbID: tmdb,
				MediaType: map[string]string{"电影": "movie", "剧集": "tv"}[mediaType],
				Category:  category,
			}
			out[key] = e
		}
		if sf.UpdatedAt.After(e.LastAt) {
			e.LastAt = sf.UpdatedAt
		}
	}
	return out
}

// parseTitleDir 解析标题目录名："Z-重器-2026-[tmdb=291856]" → (重器, 2026, 291856)
func parseTitleDir(dir string) (title, year string, tmdb int) {
	tmdb = 0
	if m := regexp.MustCompile(`\[tmdb=(\d+)\]`).FindStringSubmatch(dir); m != nil {
		tmdb, _ = strconv.Atoi(m[1])
	}
	year = ""
	if m := regexp.MustCompile(`(?:^|[-. ])((?:19|20)\d{2})(?:$|[-. ])`).FindStringSubmatch(dir); m != nil {
		year = m[1]
	}
	title = dir
	title = regexp.MustCompile(`\[tmdb=\d+\]`).ReplaceAllString(title, "")
	if year != "" {
		title = strings.Replace(title, year, "", 1)
	}
	title = regexp.MustCompile(`^[A-Z\d]-`).ReplaceAllString(title, "")
	title = strings.Trim(title, "- _.[]")
	if title == "" {
		title = dir
	}
	return
}

// portalNav 分类导航（按台账实际内容聚合）
func portalNav(c *gin.Context) {
	entries := portalScanLedger()
	counts := map[string]map[string]int{}
	for _, e := range entries {
		if counts[e.MediaType] == nil {
			counts[e.MediaType] = map[string]int{}
		}
		if e.Category != "" {
			counts[e.MediaType][e.Category]++
		}
	}
	nav := map[string][]map[string]interface{}{}
	for _, mt := range []string{"movie", "tv"} {
		for name, n := range counts[mt] {
			nav[mt] = append(nav[mt], map[string]interface{}{"name": name, "count": n})
		}
		sort.Slice(nav[mt], func(i, j int) bool { return nav[mt][i]["name"].(string) < nav[mt][j]["name"].(string) })
	}
	c.JSON(http.StatusOK, gin.H{"nav": nav})
}

// portalList 媒体列表（全量台账聚合；元数据从整理记录合并）
func portalList(c *gin.Context) {
	mt := c.Query("type")
	if mt != "movie" && mt != "tv" {
		mt = "movie"
	}
	page := 1
	fmt.Sscanf(c.Query("page"), "%d", &page)
	if page < 1 {
		page = 1
	}
	const size = 36
	cat := c.Query("cat")
	kw := strings.TrimSpace(c.Query("q"))

	type enriched struct {
		e     *portalTitleEntry
		poster string
		vote   float64
	}
	// 元数据批量合并（避免逐条查库）
	metaByID := map[int]model.MediaLibrary{}
	if mls := ([]model.MediaLibrary)(nil); true {
		model.DB.Where("media_type = ?", mt).Find(&mls)
		for _, m := range mls {
			if m.TmdbID > 0 {
				metaByID[m.TmdbID] = m
			}
		}
	}
	entries := make([]enriched, 0)
	for _, e := range portalScanLedger() {
		if e.MediaType != mt {
			continue
		}
		if cat != "" && e.Category != cat {
			continue
		}
		if kw != "" && !strings.Contains(e.Title, kw) {
			continue
		}
		en := enriched{e: e}
		if e.TmdbID > 0 {
			if ml, ok := metaByID[e.TmdbID]; ok {
				en.poster = ml.PosterPath
				en.vote = ml.VoteAverage
			}
		}
		entries = append(entries, en)
	}
	switch c.DefaultQuery("sort", "recent") {
	case "rating":
		sort.Slice(entries, func(i, j int) bool { return entries[i].vote > entries[j].vote })
	case "title":
		sort.Slice(entries, func(i, j int) bool { return entries[i].e.Title < entries[j].e.Title })
	default:
		sort.Slice(entries, func(i, j int) bool { return entries[i].e.LastAt.After(entries[j].e.LastAt) })
	}
	total := len(entries)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	items := []gin.H{}
	for _, en := range entries[start:end] {
		items = append(items, gin.H{"key": en.e.Key, "title": en.e.Title, "year": en.e.Year,
			"media_type": en.e.MediaType, "category": en.e.Category, "tmdb_id": en.e.TmdbID,
			"poster_path": en.poster, "vote_average": en.vote})
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "size": size, "items": items})
}

// portalDetail 详情 + 播放文件清单（key = 标题目录路径）
func portalDetail(c *gin.Context) {
	key := strings.Trim(c.Query("key"), "/")
	if key == "" || strings.Contains(key, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var sfs []model.SyncedFile
	model.DB.Where("rel_path LIKE ?", key+"/%").Limit(500).Find(&sfs)
	if len(sfs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	segs := strings.Split(key, "/")
	title, year, tmdb := parseTitleDir(segs[len(segs)-1])
	mediaType := "tv"
	if len(segs) >= 2 && segs[1] == "电影" {
		mediaType = "movie"
	}
	category := ""
	if len(segs) >= 3 {
		category = segs[2]
	}
	var ml model.MediaLibrary
	haveML := tmdb > 0 && model.DB.Where("tmdb_id = ? AND media_type = ?", tmdb, mediaType).First(&ml).Error == nil
	if !haveML && tmdb > 0 {
		ml = model.MediaLibrary{TmdbID: tmdb, Title: title, Year: year, MediaType: mediaType, Category: category}
		portalBackfillTMDB(&ml)
		haveML = ml.PosterPath != ""
	}
	poster, vote, overview := "", float64(0), ""
	if haveML {
		poster, vote, overview = ml.PosterPath, ml.VoteAverage, ml.Overview
		if ml.Title != "" {
			title = ml.Title
		}
		if ml.Year != "" {
			year = ml.Year
		}
	}
	base302 := portal302Base(c)
	files := []gin.H{}
	for _, sf := range sfs {
		if sf.Kind != "video" || sf.PickCode == "" {
			continue
		}
		files = append(files, gin.H{
			"name": filepath.Base(sf.RelPath),
			"size": sf.Size,
			"url":  base302 + "/d/" + sf.PickCode,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"media": gin.H{"title": title, "year": year, "media_type": mediaType, "category": category,
			"poster_path": poster, "vote_average": vote, "overview": overview},
		"files": files,
	})
}

// portal302Base 直链基地址：用"当前访问门户的主机 + 302 代理端口"。
// 浏览器已能访问门户即说明该主机可达；STRM 配置的域名常是内网/媒体服务器视角地址，浏览器未必可达。
func portal302Base(c *gin.Context) string {
	host := c.Request.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return fmt.Sprintf("http://%s:%d", host, portalCfg.ProxyPort)
}

// portalBackfillWorker 后台元数据补全：为台账里有 tmdb id 但缺海报/评分的条目
// 逐个从 TMDB 拉详情并落库（限速 3 秒一个，新内容 5 分钟后重扫）
func portalBackfillWorker() {
	time.Sleep(10 * time.Second) // 等服务完全就绪
	for {
		done := 0
		for _, e := range portalScanLedger() {
			if e.TmdbID <= 0 {
				continue
			}
			var ml model.MediaLibrary
			need := true
			if model.DB.Where("tmdb_id = ? AND media_type = ?", e.TmdbID, e.MediaType).First(&ml).Error == nil {
				need = ml.PosterPath == "" || ml.Overview == ""
			}
			if !need {
				continue
			}
			if model.DB.Where("tmdb_id = ? AND media_type = ?", e.TmdbID, e.MediaType).First(&ml).Error != nil {
				ml = model.MediaLibrary{TmdbID: e.TmdbID, Title: e.Title, Year: e.Year,
					MediaType: e.MediaType, Category: e.Category, TargetPath: e.Key}
			} else if ml.TargetPath == "" {
				ml.TargetPath = e.Key
			}
			portalBackfillTMDB(&ml)
			model.DB.Save(&ml)
			done++
			time.Sleep(3 * time.Second) // 限速，避免 TMDB 配额
			if done >= 200 {
				break
			}
		}
		sleep := 5 * time.Minute
		if done > 0 {
			sleep = 30 * time.Second // 还有活干就快点回来
		}
		time.Sleep(sleep)
	}
}

// portalBackfillTMDB 从 TMDB 补海报/评分/简介（回填失败静默）
func portalBackfillTMDB(m *model.MediaLibrary) {
	tc, err := loadTmdbClient(nil)
	if err != nil {
		return
	}
	kind := "movie"
	if m.MediaType == "tv" {
		kind = "tv"
	}
	body, err := tc.get(fmt.Sprintf("/%s/%d", kind, m.TmdbID), map[string]string{"language": "zh-CN"})
	if err != nil {
		return
	}
	var d struct {
		PosterPath  string  `json:"poster_path"`
		Overview    string  `json:"overview"`
		VoteAverage float64 `json:"vote_average"`
	}
	if json.Unmarshal(body, &d) != nil {
		return
	}
	if d.PosterPath != "" {
		m.PosterPath = d.PosterPath
	}
	if d.Overview != "" {
		m.Overview = d.Overview
	}
	m.VoteAverage = d.VoteAverage
	model.DB.Save(m)
}

// portalPoster 海报代理：TMDB 图片经本服务转发并缓存（局网客户端免翻墙）
func portalPoster(c *gin.Context) {
	p := strings.TrimPrefix(c.Param("path"), "/")
	if p == "" || strings.Contains(p, "..") {
		c.String(http.StatusBadRequest, "bad path")
		return
	}
	cacheDir := filepath.Join(portalCfg.DataDir, "posters")
	_ = os.MkdirAll(cacheDir, 0755)
	h := sha1.Sum([]byte(p))
	cacheFile := filepath.Join(cacheDir, hex.EncodeToString(h[:8])+filepath.Ext(p))
	if st, err := os.Stat(cacheFile); err == nil && st.Size() > 0 && time.Since(st.ModTime()) < 7*24*time.Hour {
		c.Header("Cache-Control", "public, max-age=604800")
		c.File(cacheFile)
		return
	}
	// 服务端拉取（走代理配置）
	url := tmdbImageBase() + "/t/p/w500" + p
	client := &http.Client{Timeout: 10 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if pr, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: pr}
		}
	}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		// 占位图：1x1 透明像素，避免卡片裂图
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "image/gif", []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if len(data) > 0 {
		_ = os.WriteFile(cacheFile, data, 0644)
	}
	c.Header("Cache-Control", "public, max-age=604800")
	c.Data(http.StatusOK, resp.Header.Get("Content-Type"), data)
}

// portalPage 门户页面（内嵌单文件，暗色影院风）
func portalPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(portalHTML))
}

const portalHTML = `<!DOCTYPE html>
<html lang="zh-CN" data-theme="light">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>StrmHub 影院</title>
<style>
:root[data-theme=light]{--bg:#f5f6f8;--card:#fff;--text:#1f2328;--dim:#6a737d;--acc:#2563eb;--bd:#e4e7eb;--hover:#f0f3f6;--mask:rgba(0,0,0,.55)}
:root[data-theme=dark]{--bg:#0d1117;--card:#161b22;--text:#e6edf3;--dim:#8b949e;--acc:#4f8cff;--bd:#21262d;--hover:#1c2129;--mask:rgba(0,0,0,.75)}
*{margin:0;padding:0;box-sizing:border-box}
body{background:var(--bg);color:var(--text);font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;transition:background .2s}
a{color:var(--acc);text-decoration:none}
header{position:sticky;top:0;z-index:50;background:var(--card);padding:12px 24px;display:flex;align-items:center;gap:14px;border-bottom:1px solid var(--bd)}
header h1{font-size:19px;background:linear-gradient(90deg,#2563eb,#7c3aed);-webkit-background-clip:text;background-clip:text;color:transparent;cursor:pointer;flex:none}
.tabs{display:flex;gap:4px}
.tab{padding:6px 16px;border-radius:20px;cursor:pointer;font-size:14px;color:var(--dim)}
.tab.on{background:var(--acc);color:#fff}
#theme{border:1px solid var(--bd);background:var(--card);color:var(--text);border-radius:20px;padding:6px 14px;cursor:pointer;font-size:14px;flex:none}
#kw{width:200px;background:var(--bg);border:1px solid var(--bd);border-radius:20px;padding:8px 16px;color:var(--text);font-size:14px;outline:none;margin-left:auto}
#kw:focus{border-color:var(--acc)}
#content{padding:0 24px 40px;max-width:1500px;margin:0 auto}
/* 横向节目单 */
.row{margin-top:26px}
.row .rh{display:flex;align-items:baseline;gap:12px;margin-bottom:12px}
.row .rh h2{font-size:18px}
.row .rh .more-link{font-size:13px;color:var(--dim);cursor:pointer}
.row .rh .more-link:hover{color:var(--acc)}
.strip{display:flex;gap:14px;overflow-x:auto;padding-bottom:8px;scrollbar-width:thin}
.strip .card{flex:0 0 148px}
/* 海报网格 */
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(148px,1fr));gap:14px;margin-top:16px}
.card{background:var(--card);border-radius:10px;overflow:hidden;cursor:pointer;transition:transform .15s;border:1px solid var(--bd);position:relative}
.card:hover{transform:translateY(-4px)}
.card img{width:100%;aspect-ratio:2/3;object-fit:cover;display:block;background:var(--hover)}
.card .ph{width:100%;aspect-ratio:2/3;display:flex;align-items:center;justify-content:center;font-size:36px;background:var(--hover)}
.card .info{padding:8px 10px}
.card .t{font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.card .y{font-size:11px;color:var(--dim);margin-top:2px;display:flex;align-items:center;gap:6px}
.rate{color:#f59e0b;font-size:11px}
.badge-new{position:absolute;top:8px;left:8px;background:#ef4444;color:#fff;font-size:10px;padding:2px 7px;border-radius:9px}
.prog{position:absolute;left:0;right:0;bottom:0;height:3px;background:rgba(255,255,255,.3)}
.prog i{display:block;height:100%;background:var(--acc)}
.pct{position:absolute;left:0;right:0;bottom:6px;font-size:10px;color:#fff;text-shadow:0 1px 2px rgba(0,0,0,.8);padding-left:6px}
/* 筛选条 */
.filters{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-top:14px}
.chips{display:flex;gap:8px;overflow-x:auto;flex:1;scrollbar-width:none;padding:2px 0}
.chips::-webkit-scrollbar{display:none}
.chip{flex:none;padding:5px 14px;border-radius:16px;font-size:13px;background:var(--card);color:var(--dim);cursor:pointer;border:1px solid var(--bd)}
.chip.on{color:#fff;background:var(--acc);border-color:var(--acc)}
select{background:var(--card);color:var(--text);border:1px solid var(--bd);border-radius:16px;padding:5px 10px;font-size:13px;outline:none}
.listtitle{font-size:20px;margin-top:22px}
.empt{color:var(--dim);text-align:center;padding:60px 0}
.loadmore{text-align:center;padding:16px;color:var(--dim);cursor:pointer;font-size:14px}
.loadmore:hover{color:var(--acc)}
/* 详情弹窗 */
#mask{position:fixed;inset:0;background:var(--mask);z-index:100;display:none;align-items:center;justify-content:center;padding:24px}
#modal{background:var(--card);border-radius:14px;max-width:960px;width:100%;max-height:92vh;overflow-y:auto;border:1px solid var(--bd)}
.dhead{display:flex;gap:20px;padding:24px}
.dhead img{width:190px;border-radius:8px;aspect-ratio:2/3;object-fit:cover;background:var(--hover);flex:none}
.dbody{flex:1;min-width:0}
.dbody h2{font-size:22px;margin-bottom:6px}
.meta{color:var(--dim);font-size:13px;margin-bottom:4px}
.badges span{display:inline-block;padding:2px 10px;border-radius:12px;font-size:12px;background:rgba(37,99,235,.12);color:var(--acc);margin:6px 6px 0 0}
.ov{font-size:13px;line-height:1.8;color:var(--dim);margin-top:10px;max-height:130px;overflow-y:auto}
.playbtns{margin-top:14px;display:flex;gap:10px;flex-wrap:wrap}
.play{padding:10px 28px;background:linear-gradient(90deg,#2563eb,#7c3aed);border:none;border-radius:22px;color:#fff;font-size:15px;cursor:pointer}
.ghost{padding:10px 18px;background:var(--card);border:1px solid var(--bd);border-radius:22px;color:var(--text);font-size:13px;cursor:pointer}
.eplist{padding:0 24px 24px}
.eplist h3{font-size:14px;margin:10px 0}
.ep{display:flex;align-items:center;gap:10px;padding:8px 12px;border-radius:8px;cursor:pointer;font-size:13px}
.ep:hover{background:var(--hover)}
.ep.playing{background:rgba(37,99,235,.12)}
.ep .p2{color:var(--dim);margin-left:auto;font-size:11px}
#player{width:100%;background:#000;aspect-ratio:16/9;display:none}
#player video{width:100%;height:100%}
.warn{padding:10px 24px;color:#d97706;font-size:12px;display:none}
.fail{padding:14px 24px;font-size:13px;color:var(--dim);display:none;line-height:2}
.fail input{width:70%;background:var(--bg);border:1px solid var(--bd);border-radius:8px;padding:8px 10px;color:var(--text);font-size:12px}
.close{position:sticky;float:right;top:10px;right:10px;z-index:5;font-size:22px;color:var(--dim);cursor:pointer;padding:4px 10px;background:var(--card);border-radius:50%}
@media(max-width:600px){
 #content{padding:0 12px 40px}.grid{grid-template-columns:repeat(3,1fr);gap:10px}.strip .card{flex:0 0 110px}
 .dhead{flex-direction:column}.dhead img{width:130px}#kw{width:110px}header{padding:10px 12px;gap:8px}header h1{font-size:16px}
}
</style>
</head>
<body>
<header>
  <h1 onclick="goHome()">🎬 StrmHub 影院</h1>
  <div class="tabs">
    <div class="tab" data-t="movie" onclick="goType('movie')">电影</div>
    <div class="tab" data-t="tv" onclick="goType('tv')">剧集</div>
  </div>
  <input id="kw" placeholder="搜索片名…">
  <button id="theme" onclick="toggleTheme()">🌙</button>
</header>
<div id="content"></div>

<div id="mask" onclick="if(event.target===this)closeDetail()">
  <div id="modal">
    <span class="close" onclick="closeDetail()">✕</span>
    <div id="player"><video id="video" controls playsinline></video></div>
    <div class="warn" id="warn">⚠ 浏览器仅支持直出 MP4/WebM（H.264）；MKV/H.265 大概率无法播放，属浏览器限制。可复制直链用 PotPlayer/VLC/nPlayer 打开。</div>
    <div class="fail" id="fail">播放失败：浏览器不支持该视频格式。<br>直链：<input id="faillink" readonly><button class="ghost" onclick="copyFailLink()">复制链接</button> <span id="copyok" style="color:#16a34a"></span><br>推荐用该链接配合本地播放器（PotPlayer/VLC/nPlayer/Infuse）观看，或在 Emby 客户端播放（支持转码）。</div>
    <div class="dhead">
      <img id="d-poster">
      <div class="dbody">
        <h2 id="d-title"></h2>
        <div class="meta" id="d-meta"></div>
        <div class="badges" id="d-badges"></div>
        <div class="ov" id="d-ov"></div>
        <div class="playbtns">
          <button class="play" id="d-play" onclick="playIdx(0)">▶ 播放</button>
          <button class="ghost" onclick="copyCurLink()">复制直链</button>
        </div>
      </div>
    </div>
    <div class="eplist" id="eplist"></div>
  </div>
</div>
<script>
const $=id=>document.getElementById(id);
let view='home',curType='movie',curCat='',curSort='recent',curPage=1,curFiles=[],curMedia=null,curKey='';
const PAGE=24;

/* ---------- 主题 ---------- */
function applyTheme(t){document.documentElement.dataset.theme=t;$('theme').textContent=t==='dark'?'☀️':'🌙';try{localStorage.setItem('pt',t)}catch(e){}}
function toggleTheme(){applyTheme(document.documentElement.dataset.theme==='dark'?'light':'dark')}
(function(){var t='light';try{t=localStorage.getItem('pt')||'light'}catch(e){}applyTheme(t)})();

/* ---------- 继续观看（本地进度） ---------- */
function getProg(){try{return JSON.parse(localStorage.getItem('pprog')||'{}')}catch(e){return{}}}
function setProg(k,v){var p=getProg();p[k]=v;try{localStorage.setItem('pprog',JSON.stringify(p))}catch(e){}}
function delProg(k){var p=getProg();delete p[k];try{localStorage.setItem('pprog',JSON.stringify(p))}catch(e){}}
function recentProg(){return Object.values(getProg()).sort((a,b)=>b.ts-a.ts).slice(0,12)}

/* ---------- 路由 ---------- */
function goHome(){view='home';curCat='';renderTabs('');renderHome()}
function goType(t){curType=t;view='list';curCat='';curSort='recent';curPage=1;renderTabs(t);renderList()}
function goCat(t,c){curType=t;view='list';curCat=c;curSort='recent';curPage=1;renderTabs(t);renderList()}
function renderTabs(on){document.querySelectorAll('.tab').forEach(x=>x.classList.toggle('on',x.dataset.t===on))}
let kwTimer;
$('kw').oninput=()=>{clearTimeout(kwTimer);kwTimer=setTimeout(()=>{if(!$('kw').value.trim())return;view='search';renderSearch()},350)};

/* ---------- 数据 ---------- */
async function fetchList(o){
  const u=new URL('/api/portal/list',location);u.searchParams.set('type',o.type||curType);
  if(o.cat)u.searchParams.set('cat',o.cat);if(o.q)u.searchParams.set('q',o.q);
  u.searchParams.set('sort',o.sort||'recent');u.searchParams.set('page',o.page||1);u.searchParams.set('size',o.size||PAGE);
  return (await fetch(u)).json()
}
async function fetchNav(){return (await fetch('/api/portal/nav')).json().nav||{}}

/* ---------- 渲染 ---------- */
function cardHTML(m,extra){
  const poster=m.poster_path?('<img loading="lazy" src="/poster'+m.poster_path+'">'):'<div class="ph">🎬</div>';
  const rate=m.vote_average>0?('<span class="rate">★ '+m.vote_average.toFixed(1)+'</span>'):'';
  return '<div class="card" onclick="openDetail(\''+escA(m.key)+'\')">'+(extra&&extra.isNew?'<span class="badge-new">新</span>':'')+poster+
    (extra&&extra.pct!==undefined?'<div class="prog"><i style="width:'+extra.pct+'%"></i></div><div class="pct">看到 '+extra.pct+'%</div>':'')+
    '<div class="info"><div class="t">'+esc(m.title)+'</div><div class="y">'+(m.year||'')+' '+rate+'</div></div></div>'
}
function esc(s){return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;')}
function escA(s){return String(s||'').replace(/'/g,"\\'")}

async function renderHome(){
  const c=$('content');c.innerHTML='<div class="empt">加载中…</div>';
  const nav=await fetchNav();
  const [rc,TR,pp]=await Promise.all([
    fetchList({type:curType,sort:'recent',size:12}),
    fetchList({type:curType,sort:'rating',size:12}),
    Promise.resolve(recentProg())
  ]);
  let h='';
  // 快捷分类
  const cats=(nav[curType]||[]).slice(0,10);
  if(cats.length){
    h+='<div class="row"><div class="rh"><h2>分类</h2></div><div class="chips">'+
      cats.map(x=>'<div class="chip" onclick="goCat(\''+curType+'\',\''+escA(x.name)+'\')">'+x.name+' · '+x.count+'</div>').join('')+'</div></div>'
  }
  // 继续观看
  if(pp.length){
    h+='<div class="row"><div class="rh"><h2>▶ 继续观看</h2></div><div class="strip">'+
      pp.map(x=>'<div class="card" onclick="resume(\''+escA(x.key)+'\','+x.fileIdx+')">'+
        (x.poster?('<img src="/poster'+x.poster+'">'):'<div class="ph">🎬</div>')+
        '<div class="prog"><i style="width:'+x.pct+'%"></i></div><div class="pct">'+x.pct+'%</div>'+
        '<div class="info"><div class="t">'+esc(x.title)+'</div><div class="y">'+esc(x.epName||'')+'</div></div></div>').join('')+'</div></div>'
  }
  // 最近入库
  if((rc.items||[]).length){
    h+='<div class="row"><div class="rh"><h2>🆕 最近入库</h2><span class="more-link" onclick="goType(\''+curType+'\')">查看全部 ›</span></div><div class="strip">'+
      rc.items.map(m=>cardHTML(m,{isNew:true})).join('')+'</div></div>'
  }
  // 评分最高
  if((TR.items||[]).length){
    h+='<div class="row"><div class="rh"><h2>⭐ 评分最高</h2><span class="more-link" onclick="sortAll()">按评分浏览 ›</span></div><div class="strip">'+
      TR.items.map(m=>cardHTML(m)).join('')+'</div></div>'
  }
  if(!h)h='<div class="empt">媒体库还是空的——完成一次全量同步后，这里会出现你的全部影视</div>';
  c.innerHTML=h
}
function sortAll(){view='list';curSort='rating';curPage=1;renderTabs(curType);renderList()}

async function renderList(){
  const c=$('content');
  const nav=await fetchNav();
  const cats=nav[curType]||[];
  let h='<div class="listtitle">'+(curType==='movie'?'电影':'剧集')+(curCat?' · '+curCat:'')+'</div>';
  h+='<div class="filters"><div class="chips"><div class="chip'+(curCat===''?' on':'')+'" onclick="goCat(\''+curType+'\',\'\')">全部</div>'+
    cats.map(x=>'<div class="chip'+(curCat===x.name?' on':'')+'" onclick="goCat(\''+curType+'\',\''+escA(x.name)+'\')">'+x.name+'</div>').join('')+'</div>'+
    '<select onchange="curSort=this.value;curPage=1;renderList()"><option value="recent"'+(curSort==='recent'?' selected':'')+'>最近入库</option><option value="rating"'+(curSort==='rating'?' selected':'')+'>评分最高</option><option value="title"'+(curSort==='title'?' selected':'')+'>名称</option></select></div>';
  h+='<div class="grid" id="lgrid"></div><div class="loadmore" id="more" onclick="loadMore()">加载更多</div>';
  c.innerHTML=h;
  const d=await fetchList({cat:curCat,sort:curSort,page:1});
  paintGrid(d)
}
async function loadMore(){curPage++;const d=await fetchList({cat:curCat,sort:curSort,page:curPage});paintGrid(d,true)}
function paintGrid(d,append){
  const g=$('lgrid');
  if(!append)g.innerHTML='';
  (d.items||[]).forEach(m=>{const div=document.createElement('div');div.innerHTML=cardHTML(m);g.appendChild(div.firstChild)});
  $('more').style.display=(d.page*d.size<d.total)?'':'none';
  if(!append&&!d.items.length)g.innerHTML='<div class="empt" style="grid-column:1/-1">没有符合条件的影视</div>'
}
async function renderSearch(){
  const q=$('kw').value.trim();const c=$('content');
  c.innerHTML='<div class="listtitle">搜索：'+esc(q)+'</div><div class="grid" id="lgrid"></div>';
  const [a,b]=await Promise.all([fetchList({type:'movie',q}),fetchList({type:'tv',q})]);
  const g=$('lgrid');(a.items||[]).concat(b.items||[]).forEach(m=>{const div=document.createElement('div');div.innerHTML=cardHTML(m);g.appendChild(div.firstChild)});
  if(!(a.items||[]).length&&!(b.items||[]).length)g.innerHTML='<div class="empt" style="grid-column:1/-1">未找到相关影视</div>'
}

/* ---------- 详情与播放 ---------- */
async function openDetail(key){
  const d=await(await fetch('/api/portal/detail?key='+encodeURIComponent(key))).json();
  curMedia=d.media;curFiles=d.files||[];curKey=key;
  $('d-poster').src=curMedia.poster_path?('/poster'+curMedia.poster_path):'';
  $('d-title').textContent=curMedia.title+(curMedia.year?'（'+curMedia.year+'）':'');
  $('d-meta').textContent=(curMedia.media_type==='tv'?'剧集':'电影')+(curMedia.category?' · '+curMedia.category:'');
  $('d-badges').innerHTML=(curMedia.vote_average>0?'<span>★ '+curMedia.vote_average.toFixed(1)+'</span>':'')+
    '<span>'+(curFiles.length||0)+' 个视频</span>';
  $('d-ov').textContent=curMedia.overview||'暂无简介';
  const ep=$('eplist');
  if(curFiles.length>1){
    ep.innerHTML='<h3>选集</h3>'+curFiles.map((f,i)=>{
      const p=getProg()[key+'#'+i];
      return '<div class="ep" id="ep-'+i+'" onclick="playIdx('+i+')"><span>'+esc(f.name)+'</span>'+
      (p?'<span style="color:var(--acc);font-size:11px">已看 '+p.pct+'%</span>':'')+
      '<span class="p2">'+(f.size>0?(f.size/1073741824).toFixed(1)+' GB':'')+'</span></div>'}).join('')
  }else ep.innerHTML='';
  $('player').style.display='none';$('warn').style.display='none';$('fail').style.display='none';
  $('mask').style.display='flex'
}
function closeDetail(){saveProgress(true);$('mask').style.display='none';const v=$('video');v.pause();v.removeAttribute('src');v.load()}
function playIdx(i,resumePct){
  const f=curFiles[i];if(!f){alert('该条目暂无视频文件');return}
  saveProgress(true);
  document.querySelectorAll('.ep').forEach(e=>e.classList.remove('playing'));
  const el=$('ep-'+i);if(el)el.classList.add('playing');
  const v=$('video');curFileIdx=i;
  v.src=f.url;curFileUrl=f.url;
  v.onerror=()=>{$('fail').style.display='block';$('faillink').value=f.url};
  v.onloadedmetadata=()=>{if(resumePct>0&&v.duration)v.currentTime=v.duration*resumePct/100};
  v.play().catch(()=>{});
  $('player').style.display='block';$('warn').style.display='';$('fail').style.display='none';
  $('player').scrollIntoView({behavior:'smooth'});
  // 进度记录
  clearInterval(progTimer);
  progTimer=setInterval(()=>saveProgress(false),5000)
}
let curFileIdx=0,curFileUrl='',progTimer=null;
function saveProgress(final){
  if(!curKey||!curFileUrl)return;
  const v=$('video');if(!v.duration)return;
  const pct=Math.min(100,Math.round(v.currentTime/v.duration*100));
  if(pct>=95){delProg(curKey+'#'+curFileIdx);return}
  setProg(curKey+'#'+curFileIdx,{key:curKey,fileIdx:curFileIdx,title:curMedia?curMedia.title:'',epName:(curFiles[curFileIdx]||{}).name||'',poster:curMedia?curMedia.poster_path:'',pct:pct,ts:Date.now()});
  if(final)clearInterval(progTimer)
}
async function resume(key,fileIdx){
  await openDetail(key);
  const p=getProg()[key+'#'+fileIdx];
  playIdx(fileIdx,p?p.pct:0)
}
function copyCurLink(){if(curFileUrl)copyTxt(curFileUrl)}
function copyFailLink(){copyTxt($('faillink').value)}
function copyTxt(t){
  if(navigator.clipboard)navigator.clipboard.writeText(t).then(()=>{$('copyok').textContent='已复制';setTimeout(()=>$('copyok').textContent='',1500)});
  else{const i=$('faillink');i.select();document.execCommand('copy')}
}

/* ---------- 启动 ---------- */
goHome();
</script>
</body>
</html>`
