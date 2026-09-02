package api

// ==================== MetaTube AV 番号识别 ====================
//
// 对接自部署的 metatube-server（github.com/metatube-community/metatube）：
//   GET /v1/movies/search?q=<番号>          → []MovieSummary（各刮削源命中列表）
//   GET /v1/movies/{provider}/{id}          → Movie 详情（标题/封面/演员/类型…）
//   GET /v1/providers                       → 启用的刮削源（测试连接用）
// 认证：URL 上带 ?token=（服务端配置了 MT_TOKEN 时必填）。
//
// 定位 = AV 版的 TMDB 识别：整理流程（processAVDirectory）番号提取后自动识别，
// 结果缓存 AVMeta 表，失败/未配置自动退回纯番号行为。识别结果只用于
// 重命名模板变量 {av_title}/{av_year}/{actor}/{actors}（缓存直读，不触发网络）
// 与总览面板展示，不向媒体目录写任何元数据文件。

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
	req, err := nethttp.NewRequest(nethttp.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := metatubeClient.Do(req)
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

// posterCacheFile 海报磁盘缓存路径——serveTMDBPoster（总览/封面代理）
// 与 readPosterCache（媒体库海报插件）必须用同一 key 推导
func posterCacheFile(dataDir, key string) string {
	cacheDir := filepath.Join(dataDir, "posters")
	_ = os.MkdirAll(cacheDir, 0o755)
	h := sha1.Sum([]byte(key))
	return filepath.Join(cacheDir, hex.EncodeToString(h[:8])+path.Ext(key))
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
	// 第一段：/v1/providers（公开端点）验证地址可达
	var providers struct {
		MovieProviders json.RawMessage `json:"movie_providers"`
		ActorProviders json.RawMessage `json:"actor_providers"`
	}
	if err := metatubeGet(cfg, "/v1/providers", &providers); err != nil {
		c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "服务器不可达: " + truncateStr(err.Error(), 150)})
		return
	}
	// 第二段：/v1/movies/search（token 保护端点）验证 token 真实有效。
	// providers 不校验 token，只测它会出现"随便填 token 也成功"的假象
	var probe []metatubeMovie
	if err := metatubeGet(cfg, "/v1/movies/search?q=test", &probe); err != nil {
		if strings.Contains(err.Error(), "401") {
			c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "Token 错误：服务器已开启认证，请填写部署时设置的 MT_TOKEN"})
		} else {
			c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "搜索接口异常: " + truncateStr(err.Error(), 150)})
		}
		return
	}
	n := len(metatubeJSONStrings(providers.MovieProviders)) + len(metatubeJSONStrings(providers.ActorProviders))
	msg := fmt.Sprintf("连接成功（%dms）", time.Since(start).Milliseconds())
	if n > 0 {
		msg += fmt.Sprintf("，已启用 %d 个刮削源", n)
	}
	c.JSON(nethttp.StatusOK, gin.H{"success": true, "message": msg})
}
