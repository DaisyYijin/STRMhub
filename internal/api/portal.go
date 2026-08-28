package api

// ==================== 观影门户（6666） ====================
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

// portalNav 分类导航（规则表顺序）
func portalNav(c *gin.Context) {
	var rules []model.CategoryRule
	model.DB.Order("media_type ASC, priority ASC").Find(&rules)
	nav := map[string][]map[string]interface{}{}
	for _, r := range rules {
		var count int64
		model.DB.Model(&model.MediaLibrary{}).Where("media_type = ? AND category = ?", r.MediaType, r.Name).Count(&count)
		nav[r.MediaType] = append(nav[r.MediaType], map[string]interface{}{
			"name": r.Name, "count": count,
		})
	}
	c.JSON(http.StatusOK, gin.H{"nav": nav})
}

// portalList 媒体列表（类型/分类/搜索/分页）
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
	q := model.DB.Model(&model.MediaLibrary{}).Where("media_type = ?", mt)
	if cat := c.Query("cat"); cat != "" {
		q = q.Where("category = ?", cat)
	}
	if kw := strings.TrimSpace(c.Query("q")); kw != "" {
		q = q.Where("title LIKE ?", "%"+kw+"%")
	}
	var total int64
	q.Count(&total)
	var items []model.MediaLibrary
	q.Order("created_at DESC").Limit(size).Offset((page - 1) * size).Find(&items)
	c.JSON(http.StatusOK, gin.H{
		"total": total, "page": page, "size": size,
		"items": items,
	})
}

// portalDetail 详情 + 播放文件清单（从同步台账取该剧/该片下的视频，带直链）
func portalDetail(c *gin.Context) {
	id := 0
	fmt.Sscanf(c.Query("id"), "%d", &id)
	var m model.MediaLibrary
	if err := model.DB.First(&m, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	// 旧记录缺海报/简介：按 TMDB 详情回填一次
	if (m.PosterPath == "" || m.Overview == "") && m.TmdbID > 0 {
		portalBackfillTMDB(&m)
	}
	// 台账取视频文件（按 TargetPath 匹配，兼容带库名前缀）
	base := strings.TrimSuffix(m.TargetPath, "/")
	var sfs []model.SyncedFile
	model.DB.Where("rel_path LIKE ?", base+"/%").Limit(200).Find(&sfs)
	if len(sfs) == 0 {
		model.DB.Where("rel_path LIKE ?", "%/"+base+"/%").Limit(200).Find(&sfs)
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
	c.JSON(http.StatusOK, gin.H{"media": m, "files": files})
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
:root{--bg:#0d1117;--card:#161b22;--text:#e6edf3;--dim:#8b949e;--acc:#4f8cff;--radius:10px}
*{margin:0;padding:0;box-sizing:border-box}
body{background:var(--bg);color:var(--text);font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif}
header{position:sticky;top:0;z-index:50;background:rgba(13,17,23,.92);backdrop-filter:blur(8px);padding:14px 24px;display:flex;align-items:center;gap:16px;border-bottom:1px solid #21262d}
header h1{font-size:20px;background:linear-gradient(90deg,#4f8cff,#a371f7);-webkit-background-clip:text;background-clip:text;color:transparent}
.tabs{display:flex;gap:4px}
.tab{padding:6px 16px;border-radius:20px;cursor:pointer;font-size:14px;color:var(--dim)}
.tab.on{background:var(--acc);color:#fff}
#kw{flex:1;max-width:320px;background:var(--card);border:1px solid #30363d;border-radius:20px;padding:8px 16px;color:var(--text);font-size:14px;outline:none;margin-left:auto}
#kw:focus{border-color:var(--acc)}
nav{position:sticky;top:57px;z-index:40;background:rgba(13,17,23,.92);padding:10px 24px;display:flex;gap:8px;overflow-x:auto;scrollbar-width:none}
nav::-webkit-scrollbar{display:none}
.cat{flex:none;padding:5px 14px;border-radius:16px;font-size:13px;background:var(--card);color:var(--dim);cursor:pointer;border:1px solid transparent}
.cat.on{color:var(--acc);border-color:var(--acc)}
main{padding:20px 24px;display:grid;grid-template-columns:repeat(auto-fill,minmax(150px,1fr));gap:16px}
.card{background:var(--card);border-radius:var(--radius);overflow:hidden;cursor:pointer;transition:transform .15s}
.card:hover{transform:translateY(-4px)}
.card img{width:100%;aspect-ratio:2/3;object-fit:cover;display:block;background:#21262d}
.card .info{padding:8px 10px}
.card .t{font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.card .y{font-size:11px;color:var(--dim);margin-top:2px}
.more{grid-column:1/-1;text-align:center;padding:14px;color:var(--dim);cursor:pointer;font-size:14px}
.more:hover{color:var(--acc)}
#mask{position:fixed;inset:0;background:rgba(0,0,0,.75);z-index:100;display:none;align-items:center;justify-content:center;padding:24px}
#modal{background:var(--card);border-radius:14px;max-width:960px;width:100%;max-height:92vh;overflow-y:auto}
.dhead{display:flex;gap:20px;padding:24px}
.dhead img{width:200px;border-radius:8px;aspect-ratio:2/3;object-fit:cover;background:#21262d;flex:none}
.dbody{flex:1;min-width:0}
.dbody h2{font-size:22px;margin-bottom:6px}
.meta{color:var(--dim);font-size:13px;margin-bottom:4px}
.badge{display:inline-block;padding:2px 10px;border-radius:12px;font-size:12px;background:rgba(79,140,255,.15);color:var(--acc);margin:6px 6px 0 0}
.ov{font-size:13px;line-height:1.8;color:var(--dim);margin-top:10px}
.play{margin-top:14px;padding:10px 28px;background:linear-gradient(90deg,#4f8cff,#a371f7);border:none;border-radius:22px;color:#fff;font-size:15px;cursor:pointer}
.eplist{padding:0 24px 24px}
.eplist h3{font-size:14px;margin:10px 0}
.ep{display:flex;align-items:center;gap:10px;padding:8px 12px;border-radius:8px;cursor:pointer;font-size:13px}
.ep:hover{background:#21262d}
.ep.playing{background:rgba(79,140,255,.15)}
#player{width:100%;background:#000;aspect-ratio:16/9;display:none}
#player video{width:100%;height:100%}
.warn{padding:10px 24px;color:#f0ad4e;font-size:12px;display:none}
.close{position:sticky;float:right;top:10px;right:10px;z-index:5;font-size:22px;color:var(--dim);cursor:pointer;padding:4px 10px}
@media(max-width:600px){main{grid-template-columns:repeat(3,1fr);gap:10px;padding:12px}.dhead{flex-direction:column}.dhead img{width:140px}#kw{max-width:140px}}
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
</header>
<nav id="cats"></nav>
<main id="grid"></main>
<div class="more" id="more" onclick="loadMore()">加载更多</div>

<div id="mask" onclick="if(event.target===this)closeDetail()">
  <div id="modal">
    <span class="close" onclick="closeDetail()">✕</span>
    <div id="player"><video id="video" controls playsinline></video></div>
    <div class="warn" id="warn">⚠ 当前浏览器可能不支持该视频编码（如 MKV/H.265）。无法播放属正常限制，请在 Emby 客户端观看。</div>
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
  $('cats').innerHTML='<div class="cat'+(curCat===''?' on':'')+'" onclick="setCat(\'\')">全部</div>'+
    list.filter(c=>c.count>0).map(c=>'<div class="cat'+(curCat===c.name?' on':'')+'" onclick="setCat(\''+c.name+'\')">'+c.name+' '+c.count+'</div>').join('');
}
function setCat(c){curCat=c;curPage=1;loadNav();load()}

async function load(){
  const u='/api/portal/list?type='+curType+'&page='+curPage+'&cat='+encodeURIComponent(curCat)+'&q='+encodeURIComponent($('kw').value);
  const d=await(await fetch(u)).json();
  const g=$('grid');
  if(curPage===1)g.innerHTML='';
  (d.items||[]).forEach(m=>{
    const el=document.createElement('div');el.className='card';
    const poster=m.poster_path?('/poster'+m.poster_path):'';
    el.innerHTML=(poster?'<img loading="lazy" src="'+poster+'">':'<img style="display:flex;align-items:center;justify-content:center;color:#30363d;font-size:40px">🎬</img>')+
      '<div class="info"><div class="t">'+esc(m.title)+'</div><div class="y">'+(m.year||'')+' · '+(m.vote_average>0?m.vote_average.toFixed(1)+'分':'')+'</div></div>';
    el.onclick=()=>openDetail(m.id);
    g.appendChild(el);
  });
  $('more').style.display=(d.page*d.size<d.total)?'':'none';
}
function loadMore(){curPage++;load()}
function esc(s){return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;')}

async function openDetail(id){
  const d=await(await fetch('/api/portal/detail?id='+id)).json();
  const m=d.media;curFiles=d.files||[];
  $('d-poster').src=m.poster_path?('/poster'+m.poster_path):'';
  $('d-title').textContent=m.title+(m.year?'（'+m.year+'）':'');
  $('d-meta').textContent=(m.media_type==='tv'?'剧集':'电影')+' · '+(m.category||'');
  $('d-badges').innerHTML=(m.vote_average>0?'<span class="badge">⭐ '+m.vote_average.toFixed(1)+'</span>':'')+
    (curFiles.length?'<span class="badge">'+curFiles.length+' 个视频</span>':'<span class="badge">暂无视频文件</span>');
  $('d-ov').textContent=m.overview||'暂无简介';
  const ep=$('eplist');
  if(curFiles.length>1){
    ep.innerHTML='<h3>选集</h3>'+curFiles.map((f,i)=>'<div class="ep" id="ep-'+i+'" onclick="playIdx('+i+')">'+esc(f.name)+' <span style="color:var(--dim);margin-left:auto">'+(f.size>0?(f.size/1073741824).toFixed(1)+' GB':'')+'</span></div>').join('');
  }else ep.innerHTML='';
  $('player').style.display='none';$('warn').style.display='none';
  $('mask').style.display='flex';
}
function closeDetail(){$('mask').style.display='none';$('video').pause();$('video').removeAttribute('src')}
function playIdx(i){
  const f=curFiles[i];if(!f)return;
  document.querySelectorAll('.ep').forEach(e=>e.classList.remove('playing'));
  const el=$('ep-'+i);if(el)el.classList.add('playing');
  const v=$('video');
  v.src=f.url;v.play().catch(()=>{});
  $('player').style.display='block';
  $('warn').style.display='';
  $('player').scrollIntoView({behavior:'smooth'});
}
loadNav();load();
</script>
</body>
</html>`
