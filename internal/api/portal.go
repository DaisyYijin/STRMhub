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

	entries := make([]*portalTitleEntry, 0)
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
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LastAt.After(entries[j].LastAt) })
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
	for _, e := range entries[start:end] {
		it := gin.H{"key": e.Key, "title": e.Title, "year": e.Year,
			"media_type": e.MediaType, "category": e.Category, "tmdb_id": e.TmdbID,
			"poster_path": "", "vote_average": 0}
		var ml model.MediaLibrary
		if e.TmdbID > 0 && model.DB.Where("tmdb_id = ? AND media_type = ?", e.TmdbID, e.MediaType).First(&ml).Error == nil {
			it["poster_path"] = ml.PosterPath
			it["vote_average"] = ml.VoteAverage
		}
		items = append(items, it)
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

// portal302Base 直链基地址：优先 STRM 配置的直连域名，其次本请求主机换 6086 端口
func portal302Base(c *gin.Context) string {
	var strmCfg struct {
		Domain string `json:"domain"`
	}
	_ = json.Unmarshal([]byte(settingValueCompat("strm")), &strmCfg)
	if d := strings.TrimRight(strings.TrimSpace(strmCfg.Domain), "/"); d != "" && strings.Contains(d, "//") {
		return d
	}
	host := c.Request.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return fmt.Sprintf("http://%s:%d", host, portalCfg.ProxyPort)
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
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>StrmHub 影院</title>
<style>
:root[data-theme=light]{--bg:#f5f6f8;--card:#ffffff;--text:#1f2328;--dim:#6a737d;--acc:#2563eb;--bd:#e4e7eb;--hover:#f0f3f6}
:root[data-theme=dark]{--bg:#0d1117;--card:#161b22;--text:#e6edf3;--dim:#8b949e;--acc:#4f8cff;--bd:#21262d;--hover:#21262d}
*{margin:0;padding:0;box-sizing:border-box}
body{background:var(--bg);color:var(--text);font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;transition:background .2s}
header{position:sticky;top:0;z-index:50;background:var(--card);padding:14px 24px;display:flex;align-items:center;gap:14px;border-bottom:1px solid var(--bd)}
header h1{font-size:20px;background:linear-gradient(90deg,#2563eb,#7c3aed);-webkit-background-clip:text;background-clip:text;color:transparent}
.tabs{display:flex;gap:4px}
.tab{padding:6px 16px;border-radius:20px;cursor:pointer;font-size:14px;color:var(--dim)}
.tab.on{background:var(--acc);color:#fff}
#theme{border:1px solid var(--bd);background:var(--card);color:var(--text);border-radius:20px;padding:6px 14px;cursor:pointer;font-size:14px}
#kw{width:220px;background:var(--bg);border:1px solid var(--bd);border-radius:20px;padding:8px 16px;color:var(--text);font-size:14px;outline:none;margin-left:auto}
#kw:focus{border-color:var(--acc)}
nav{position:sticky;top:57px;z-index:40;background:var(--card);padding:10px 24px;display:flex;gap:8px;overflow-x:auto;scrollbar-width:none;border-bottom:1px solid var(--bd)}
nav::-webkit-scrollbar{display:none}
.cat{flex:none;padding:5px 14px;border-radius:16px;font-size:13px;background:var(--bg);color:var(--dim);cursor:pointer;border:1px solid transparent}
.cat.on{color:var(--acc);border-color:var(--acc)}
main{padding:20px 24px;display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:16px}
.card{background:var(--card);border-radius:10px;overflow:hidden;cursor:pointer;transition:transform .15s;border:1px solid var(--bd)}
.card:hover{transform:translateY(-4px)}
.card img{width:100%;aspect-ratio:2/3;object-fit:cover;display:block;background:var(--hover)}
.card .ph{width:100%;aspect-ratio:2/3;display:flex;align-items:center;justify-content:center;font-size:40px;background:var(--hover)}
.card .info{padding:8px 10px}
.card .t{font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.card .y{font-size:11px;color:var(--dim);margin-top:2px}
.more{grid-column:1/-1;text-align:center;padding:14px;color:var(--dim);cursor:pointer;font-size:14px}
.more:hover{color:var(--acc)}
#mask{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:100;display:none;align-items:center;justify-content:center;padding:24px}
#modal{background:var(--card);border-radius:14px;max-width:960px;width:100%;max-height:92vh;overflow-y:auto;border:1px solid var(--bd)}
.dhead{display:flex;gap:20px;padding:24px}
.dhead img{width:200px;border-radius:8px;aspect-ratio:2/3;object-fit:cover;background:var(--hover);flex:none}
.dbody{flex:1;min-width:0}
.dbody h2{font-size:22px;margin-bottom:6px}
.meta{color:var(--dim);font-size:13px;margin-bottom:4px}
.badge{display:inline-block;padding:2px 10px;border-radius:12px;font-size:12px;background:rgba(37,99,235,.12);color:var(--acc);margin:6px 6px 0 0}
.ov{font-size:13px;line-height:1.8;color:var(--dim);margin-top:10px}
.play{margin-top:14px;padding:10px 28px;background:linear-gradient(90deg,#2563eb,#7c3aed);border:none;border-radius:22px;color:#fff;font-size:15px;cursor:pointer}
.eplist{padding:0 24px 24px}
.eplist h3{font-size:14px;margin:10px 0}
.ep{display:flex;align-items:center;gap:10px;padding:8px 12px;border-radius:8px;cursor:pointer;font-size:13px}
.ep:hover{background:var(--hover)}
.ep.playing{background:rgba(37,99,235,.12)}
#player{width:100%;background:#000;aspect-ratio:16/9;display:none}
#player video{width:100%;height:100%}
.warn{padding:10px 24px;color:#d97706;font-size:12px;display:none}
.close{position:sticky;float:right;top:10px;right:10px;z-index:5;font-size:22px;color:var(--dim);cursor:pointer;padding:4px 10px}
@media(max-width:600px){main{grid-template-columns:repeat(3,1fr);gap:10px;padding:12px}.dhead{flex-direction:column}.dhead img{width:140px}#kw{width:130px}}
</style>
</head>
<body>
<header>
  <h1>🎬 StrmHub 影院</h1>
  <div class="tabs">
    <div class="tab on" data-t="movie">电影</div>
    <div class="tab" data-t="tv">剧集</div>
  </div>
  <input id="kw" placeholder="搜索片名…">
  <button id="theme" onclick="toggleTheme()">🌙 暗色</button>
</header>
<nav id="cats"></nav>
<main id="grid"></main>
<div class="more" id="more" onclick="loadMore()">加载更多</div>

<div id="mask" onclick="if(event.target===this)closeDetail()">
  <div id="modal">
    <span class="close" onclick="closeDetail()">✕</span>
    <div id="player"><video id="video" controls playsinline></video></div>
    <div class="warn" id="warn">⚠ 当前浏览器可能不支持该视频编码（如 MKV/H.265）。无法播放属浏览器限制，请在 Emby 客户端观看。</div>
    <div class="dhead">
      <img id="d-poster">
      <div class="dbody">
        <h2 id="d-title"></h2>
        <div class="meta" id="d-meta"></div>
        <div id="d-badges"></div>
        <div class="ov" id="d-ov"></div>
        <button class="play" id="d-play" onclick="playIdx(0)">▶ 播放</button>
      </div>
    </div>
    <div class="eplist" id="eplist"></div>
  </div>
</div>
<script>
let curType='movie',curCat='',curPage=1,curFiles=[];
const $=id=>document.getElementById(id);

// 主题：默认亮色，可切换（记忆）
function applyTheme(t){document.documentElement.dataset.theme=t;$('theme').textContent=t==='dark'?'☀️ 亮色':'🌙 暗色';try{localStorage.setItem('portal-theme',t)}catch(e){}}
function toggleTheme(){applyTheme(document.documentElement.dataset.theme==='dark'?'light':'dark')}
(function(){var t='light';try{t=localStorage.getItem('portal-theme')||'light'}catch(e){}applyTheme(t)})();

document.querySelectorAll('.tab').forEach(t=>t.onclick=()=>{
  document.querySelectorAll('.tab').forEach(x=>x.classList.remove('on'));
  t.classList.add('on');curType=t.dataset.t;curCat='';curPage=1;
  loadNav();load();
});
let kwTimer;
$('kw').oninput=()=>{clearTimeout(kwTimer);kwTimer=setTimeout(()=>{curPage=1;load()},400)};

async function loadNav(){
  const d=await(await fetch('/api/portal/nav')).json();
  const list=(d.nav[curType]||[]);
  let html='<div class="cat'+(curCat===''?' on':'')+'" onclick="setCat(\'\')">全部</div>';
  html+=list.map(c=>'<div class="cat'+(curCat===c.name?' on':'')+'" onclick="setCat(\''+c.name+'\')">'+c.name+' '+c.count+'</div>').join('');
  $('cats').innerHTML=html;
}
function setCat(c){curCat=c;curPage=1;loadNav();load()}

async function load(){
  const u='/api/portal/list?type='+curType+'&page='+curPage+'&cat='+encodeURIComponent(curCat)+'&q='+encodeURIComponent($('kw').value);
  const d=await(await fetch(u)).json();
  const g=$('grid');
  if(curPage===1)g.innerHTML='';
  (d.items||[]).forEach(m=>{
    const el=document.createElement('div');el.className='card';
    const inner=m.poster_path
      ?'<img loading="lazy" src="/poster'+m.poster_path+'">'
      :'<div class="ph">🎬</div>';
    el.innerHTML=inner+'<div class="info"><div class="t">'+esc(m.title)+'</div><div class="y">'+(m.year||'')+(m.vote_average>0?' · ⭐'+m.vote_average.toFixed(1):'')+'</div></div>';
    el.onclick=()=>openDetail(m.key);
    g.appendChild(el);
  });
  $('more').style.display=(d.page*d.size<d.total)?'':'none';
}
function loadMore(){curPage++;load()}
function esc(s){return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;')}

async function openDetail(key){
  const d=await(await fetch('/api/portal/detail?key='+encodeURIComponent(key))).json();
  const m=d.media;curFiles=d.files||[];
  $('d-poster').src=m.poster_path?('/poster'+m.poster_path):'';
  $('d-title').textContent=m.title+(m.year?'（'+m.year+'）':'');
  $('d-meta').textContent=(m.media_type==='tv'?'剧集':'电影')+(m.category?' · '+m.category:'');
  $('d-badges').innerHTML=(m.vote_average>0?'<span class="badge">⭐ '+m.vote_average.toFixed(1)+'</span>':'')+
    (curFiles.length?'<span class="badge">'+curFiles.length+' 个视频</span>':'<span class="badge">暂无视频</span>');
  $('d-ov').textContent=m.overview||'';
  const ep=$('eplist');
  if(curFiles.length>1){
    ep.innerHTML='<h3>选集</h3>'+curFiles.map((f,i)=>'<div class="ep" id="ep-'+i+'" onclick="playIdx('+i+')">'+esc(f.name)+'<span style="color:var(--dim);margin-left:auto">'+(f.size>0?(f.size/1073741824).toFixed(1)+' GB':'')+'</span></div>').join('');
  }else ep.innerHTML='';
  $('player').style.display='none';$('warn').style.display='none';
  $('mask').style.display='flex';
}
function closeDetail(){$('mask').style.display='none';const v=$('video');v.pause();v.removeAttribute('src');v.load()}
function playIdx(i){
  const f=curFiles[i];if(!f){alert('暂无可播放的视频文件');return}
  document.querySelectorAll('.ep').forEach(e=>e.classList.remove('playing'));
  const el=$('ep-'+i);if(el)el.classList.add('playing');
  const v=$('video');
  v.src=f.url;
  v.play().catch(()=>{});
  $('player').style.display='block';
  $('warn').style.display='';
  $('player').scrollIntoView({behavior:'smooth'});
}
loadNav();load();
</script>
</body>
</html>`
