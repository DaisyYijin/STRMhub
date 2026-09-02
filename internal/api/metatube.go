package api

// ==================== MetaTube AV 元数据刮削 ====================
//
// 对接自部署的 metatube-server（github.com/metatube-community/metatube）：
//   GET /v1/movies/search?q=<番号>          → []MovieSummary（各刮削源命中列表）
//   GET /v1/movies/{provider}/{id}          → Movie 详情（标题/封面/演员/类型…）
//   GET /v1/providers                       → 启用的刮削源（测试连接用）
// 认证：URL 上带 ?token=（服务端配置了 MT_TOKEN 时必填）。
//
// 整理流程接入（processAVDirectory）：番号提取后自动刮削，结果缓存 AVMeta 表，
// 失败/未配置自动退回纯番号行为；NFO + poster.jpg 写到本地媒体目录供 Emby 读取。
// 重命名模板可用 {av_title}/{av_year}/{actor}/{actors}（缓存直读，不触发网络）。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"strmhub/internal/model"
)

// MetatubeConfig MetaTube 服务器连接配置（settings 键 "metatube"）
type MetatubeConfig struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	Enabled bool   `json:"enabled"`
}

func loadMetatubeCfg() MetatubeConfig {
	out := MetatubeConfig{}
	if v := modelSettingValue("metatube"); v != "" {
		_ = json.Unmarshal([]byte(v), &out)
	}
	out.URL = strings.TrimRight(strings.TrimSpace(out.URL), "/")
	return out
}

func metatubeEnabled() bool {
	cfg := loadMetatubeCfg()
	return cfg.Enabled && cfg.URL != ""
}

// metatubeMovie metatube-server 返回的影片结构（节选实际用到的字段）。
// Actors/Genres/PreviewImages 在服务端是 datatypes.JSON，可能是数组也可能是
// JSON 字符串，用 RawMessage 接住后按 flexible 方式解析
type metatubeMovie struct {
	Provider    string          `json:"provider"`
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	TitleNumber string          `json:"title_number"`
	OriginalTitle string        `json:"original_title"`
	Description string          `json:"description"`
	ReleaseDate string          `json:"release_date"`
	Runtime     int             `json:"runtime"`
	Director    string          `json:"director"`
	Publisher   string          `json:"publisher"`
	CoverURL    string          `json:"cover_url"`
	PreviewImages json.RawMessage `json:"preview_images_url"`
	Actors      json.RawMessage `json:"actors"`
	Genres      json.RawMessage `json:"genres"`
	Score       float64         `json:"score"`
}

// metatubeJSONStrings 兼容 []string 与 "\"a\",\"b\"" 两种形态
func metatubeJSONStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		_ = json.Unmarshal([]byte(s), &list)
	}
	return list
}

// metatubeActorNames actors 是 [{name, aliases}] 数组，提取 name 列表
func metatubeActorNames(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	type actorItem struct {
		Name string `json:"name"`
	}
	var actors []actorItem
	if err := json.Unmarshal(raw, &actors); err != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			_ = json.Unmarshal([]byte(s), &actors)
		}
	}
	names := make([]string, 0, len(actors))
	for _, a := range actors {
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}
	return names
}

var metatubeClient = &nethttp.Client{Timeout: 15 * time.Second}

// metatubeGet 调用 metatube-server（自动带上 token），解析 JSON 到 out
func metatubeGet(cfg MetatubeConfig, apiPath string, out any) error {
	u := cfg.URL + apiPath
	if cfg.Token != "" {
		sep := "?"
		if strings.Contains(apiPath, "?") {
			sep = "&"
		}
		u += sep + "token=" + url.QueryEscape(cfg.Token)
	}
	resp, err := metatubeClient.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != nethttp.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 120))
	}
	return json.Unmarshal(body, out)
}

// metatubeScrapeMu 批量整理时防止并发重复刮削同一批番号（双引擎竞态）
var metatubeScrapeMu sync.Mutex

// avNotFoundTTL not_found 缓存时长：7 天后过期重试（新片源收录有滞后）
const avNotFoundTTL = 7 * 24 * time.Hour

// metatubeFetchCached 番号 → 元数据（带缓存）。只应在整理流程调用（允许触发网络）。
// 未启用/失败返回 nil，调用方退回纯番号行为
func metatubeFetchCached(num string) *model.AVMeta {
	if !metatubeEnabled() || num == "" {
		return nil
	}
	key := normalizeAVNum(num)
	if key == "" {
		return nil
	}
	// 缓存命中（ok 永久有效；not_found 过 TTL 后重刮）
	var cached model.AVMeta
	if model.DB.Where("num = ?", key).First(&cached).Error == nil {
		if cached.Status == "ok" {
			return &cached
		}
		if cached.Status == "not_found" && time.Since(cached.UpdatedAt) < avNotFoundTTL {
			return nil
		}
	}
	// 单飞：拿不到锁说明另一个整理引擎正在刮，直接退回（不阻塞整理主流程）
	if !metatubeScrapeMu.TryLock() {
		return nil
	}
	defer metatubeScrapeMu.Unlock()

	cfg := loadMetatubeCfg()
	var hits []metatubeMovie
	if err := metatubeGet(cfg, "/v1/movies/search?q="+url.QueryEscape(num), &hits); err != nil {
		log.Printf("[MetaTube] ✗ 搜索 %s 失败: %v", num, err)
		return nil
	}
	if len(hits) == 0 {
		saveAVMeta(&model.AVMeta{Num: key, Status: "not_found", UpdatedAt: time.Now()})
		return nil
	}
	// 取第一个命中（provider 优先级由服务端排序）拉详情
	sum := hits[0]
	var detail metatubeMovie
	detailPath := fmt.Sprintf("/v1/movies/%s/%s", url.PathEscape(sum.Provider), url.PathEscape(sum.ID))
	if err := metatubeGet(cfg, detailPath, &detail); err != nil {
		log.Printf("[MetaTube] ✗ 详情 %s/%s 失败: %v", sum.Provider, sum.ID, err)
		detail = sum // 详情失败就用搜索摘要（标题/封面通常已含）
	}
	year := ""
	if d := detail.ReleaseDate; len(d) >= 4 {
		year = d[:4]
	}
	meta := &model.AVMeta{
		Num: key, Status: "ok",
		Provider: detail.Provider, ProviderID: detail.ID,
		Title:       strings.TrimSpace(detail.Title),
		OriginalTitle: strings.TrimSpace(detail.OriginalTitle),
		Year:        year, ReleaseDate: detail.ReleaseDate,
		Runtime: detail.Runtime, Director: detail.Director, Publisher: detail.Publisher,
		Plot: detail.Description, Score: detail.Score, CoverURL: detail.CoverURL,
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSpace(detail.TitleNumber)
	}
	if actors := metatubeActorNames(detail.Actors); len(actors) > 0 {
		b, _ := json.Marshal(actors)
		meta.ActorsJSON = string(b)
	}
	if genres := metatubeJSONStrings(detail.Genres); len(genres) > 0 {
		b, _ := json.Marshal(genres)
		meta.GenresJSON = string(b)
	}
	saveAVMeta(meta)
	// 封面预取到海报缓存（总览面板 /api/poster/av: 直接命中）
	if meta.CoverURL != "" {
		if err := metatubeCacheCover(meta.CoverURL); err != nil {
			log.Printf("[MetaTube] ○ 封面预取失败（展示时重试）: %v", err)
		}
	}
	return meta
}

func saveAVMeta(meta *model.AVMeta) {
	if model.DB == nil {
		return
	}
	var existing model.AVMeta
	if model.DB.Where("num = ?", meta.Num).First(&existing).Error == nil {
		meta.ID = existing.ID
		model.DB.Save(meta)
		return
	}
	model.DB.Create(meta)
}

// appDataDir 海报缓存的根目录（与门户共用 DataDir）
func appDataDir() string {
	if portalCfg != nil && portalCfg.DataDir != "" {
		return portalCfg.DataDir
	}
	return "/data"
}

// posterCacheFile 海报磁盘缓存路径——serveTMDBPoster 与 metatubeCacheCover
// 必须用同一 key 推导，预取的封面才能被代理直接命中
func posterCacheFile(dataDir, key string) string {
	cacheDir := filepath.Join(dataDir, "posters")
	_ = os.MkdirAll(cacheDir, 0o755)
	h := sha1.Sum([]byte(key))
	return filepath.Join(cacheDir, hex.EncodeToString(h[:8])+path.Ext(key))
}

// metatubeCacheCover 下载封面进海报缓存（已有 7 天内缓存则跳过）
func metatubeCacheCover(coverURL string) error {
	if coverURL == "" {
		return nil
	}
	key := "av:" + coverURL
	cacheFile := posterCacheFile(appDataDir(), key)
	if st, err := os.Stat(cacheFile); err == nil && st.Size() > 0 && time.Since(st.ModTime()) < 7*24*time.Hour {
		return nil
	}
	resp, err := metatubeClient.Get(coverURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK || len(body) < 100 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return os.WriteFile(cacheFile, body, 0o644)
}

// avMetaActors 缓存行 → 演员名列表
func avMetaActors(m *model.AVMeta) []string {
	if m == nil || m.ActorsJSON == "" {
		return nil
	}
	var list []string
	_ = json.Unmarshal([]byte(m.ActorsJSON), &list)
	return list
}

func avMetaGenres(m *model.AVMeta) []string {
	if m == nil || m.GenresJSON == "" {
		return nil
	}
	var list []string
	_ = json.Unmarshal([]byte(m.GenresJSON), &list)
	return list
}

// xmlEscape NFO 是手拼 XML，文本节点里的 & < > 必须转义
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

// writeAVMetaFiles 把 NFO + poster.jpg 写到本地媒体目录（整理移动成功后调用）。
// Emby 优先读本地 NFO（需库设置启用 NFO 读取器），poster.jpg 按命名约定天然识别；
// 任一步失败只记日志，不影响入库结果
func writeAVMetaFiles(avDir string, m *model.AVMeta, num string) {
	if m == nil || m.Status != "ok" {
		return
	}
	localRoot := defaultLocalPath
	if v := modelSettingValue("full"); v != "" {
		var full struct {
			LocalPath string `json:"local_path"`
		}
		if json.Unmarshal([]byte(v), &full) == nil && full.LocalPath != "" {
			localRoot = full.LocalPath
		}
	}
	dir := filepath.Join(localRoot, filepath.FromSlash(avDir))
	if err := os.MkdirAll(dir, 0o777); err != nil {
		log.Printf("[MetaTube] ○ 元数据目录创建失败 %s: %v", dir, err)
		return
	}
	// poster.jpg：优先用预取缓存，未命中再拉一次
	if m.CoverURL != "" {
		if data := readPosterCache(m.CoverURL); len(data) > 0 {
			if err := os.WriteFile(filepath.Join(dir, "poster.jpg"), data, 0o666); err != nil {
				log.Printf("[MetaTube] ○ poster.jpg 写入失败: %v", err)
			}
		} else if err := metatubeCacheCover(m.CoverURL); err == nil {
			if data := readPosterCache(m.CoverURL); len(data) > 0 {
				_ = os.WriteFile(filepath.Join(dir, "poster.jpg"), data, 0o666)
			}
		}
	}
	// movie.nfo
	title := m.Title
	if title == "" {
		title = num
	}
	display := title
	if len(avMetaActors(m)) > 0 {
		display = num + " " + title
	}
	actors := avMetaActors(m)
	genres := avMetaGenres(m)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\" standalone=\"yes\"?>\n<movie>\n")
	b.WriteString("  <title>" + xmlEscape(display) + "</title>\n")
	b.WriteString("  <sorttitle>" + xmlEscape(num) + "</sorttitle>\n")
	if m.OriginalTitle != "" && m.OriginalTitle != title {
		b.WriteString("  <originaltitle>" + xmlEscape(m.OriginalTitle) + "</originaltitle>\n")
	}
	if m.ReleaseDate != "" {
		b.WriteString("  <premiered>" + xmlEscape(m.ReleaseDate) + "</premiered>\n")
		b.WriteString("  <releasedate>" + xmlEscape(m.ReleaseDate) + "</releasedate>\n")
	}
	if m.Year != "" {
		b.WriteString("  <year>" + xmlEscape(m.Year) + "</year>\n")
	}
	if m.Plot != "" {
		b.WriteString("  <plot>" + xmlEscape(m.Plot) + "</plot>\n")
		b.WriteString("  <outline>" + xmlEscape(m.Plot) + "</outline>\n")
	}
	if m.Runtime > 0 {
		b.WriteString(fmt.Sprintf("  <runtime>%d</runtime>\n", m.Runtime))
	}
	b.WriteString("  <uniqueid type=\"num\" default=\"true\">" + xmlEscape(num) + "</uniqueid>\n")
	if m.Publisher != "" {
		b.WriteString("  <studio>" + xmlEscape(m.Publisher) + "</studio>\n")
	}
	if m.Director != "" {
		b.WriteString("  <director>" + xmlEscape(m.Director) + "</director>\n")
	}
	for _, g := range genres {
		b.WriteString("  <genre>" + xmlEscape(g) + "</genre>\n")
	}
	for _, a := range actors {
		b.WriteString("  <actor>\n    <name>" + xmlEscape(a) + "</name>\n    <role>" + xmlEscape(a) + "</role>\n  </actor>\n")
	}
	b.WriteString("</movie>\n")
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(b.String()), 0o666); err != nil {
		log.Printf("[MetaTube] ○ movie.nfo 写入失败: %v", err)
		return
	}
	log.Printf("[MetaTube] ✓ 元数据已写入 %s（poster.jpg + movie.nfo）", avDir)
}

// readPosterCache 读海报缓存（未命中返回 nil）
func readPosterCache(coverURL string) []byte {
	data, err := os.ReadFile(posterCacheFile(appDataDir(), "av:"+coverURL))
	if err != nil {
		return nil
	}
	return data
}

// recordAVMedia AV 条目落 MediaLibrary（总览面板/最近整理展示用）。
// AV 没有 tmdb_id，去重键 = media_type + OriginalTitle（恒存归一化番号，
// Title 则存真实标题/番号）；
// PosterPath 带 av: 前缀，serveTMDBPoster 据此走封面代理
func recordAVMedia(m *model.AVMeta, num, category, targetPath string) {
	if model.DB == nil {
		return
	}
	title := num
	year := ""
	poster := ""
	overview := ""
	if m != nil && m.Status == "ok" {
		if m.Title != "" {
			title = m.Title
		}
		// 前导 / 与 TMDB 路径一致：前端按 '/api/poster' + path 直接拼接
		year, poster, overview = m.Year, "/av:"+m.CoverURL, m.Plot
	}
	var existing model.MediaLibrary
	if model.DB.Where("media_type = ? AND original_title = ?", "av", num).First(&existing).Error == nil {
		existing.Category = category
		existing.TargetPath = targetPath
		existing.Title = title
		existing.Year = year
		existing.PosterPath = poster
		existing.Overview = overview
		model.DB.Save(&existing)
		return
	}
	model.DB.Create(&model.MediaLibrary{
		Title: title, OriginalTitle: num, Year: year, MediaType: "av",
		Category: category, TargetPath: targetPath,
		PosterPath: poster, Overview: overview,
	})
}

// ==================== HTTP 接口 ====================

// MetatubeCheck 测试连接：POST {url?, token?}（表单值优先，缺省回退已保存配置）。
// 成功返回刮削源数量
func (h *Handler) MetatubeCheck(c *gin.Context) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	cfg := loadMetatubeCfg()
	if req.URL != "" {
		cfg.URL = strings.TrimRight(strings.TrimSpace(req.URL), "/")
		cfg.Token = req.Token
	}
	if cfg.URL == "" {
		c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "请先填写服务器地址"})
		return
	}
	start := time.Now()
	var providers struct {
		MovieProviders json.RawMessage `json:"movie_providers"`
		ActorProviders json.RawMessage `json:"actor_providers"`
	}
	if err := metatubeGet(cfg, "/v1/providers", &providers); err != nil {
		c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": truncateStr(err.Error(), 160)})
		return
	}
	n := len(metatubeJSONStrings(providers.MovieProviders)) + len(metatubeJSONStrings(providers.ActorProviders))
	msg := fmt.Sprintf("连接成功（%dms）", time.Since(start).Milliseconds())
	if n > 0 {
		msg += fmt.Sprintf("，已启用 %d 个刮削源", n)
	}
	c.JSON(nethttp.StatusOK, gin.H{"success": true, "message": msg})
}
