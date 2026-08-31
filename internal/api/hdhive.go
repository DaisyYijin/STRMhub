package api

// ==================== HDHive（影巢）资源站接入 ====================
//
// 能力链：TMDB 搜索条目 → 影巢按 TMDB ID 查资源 → 解锁（X-API-Key 鉴权）
// → 115 分享链接转存（复用 share.go 的 Cookie 通道四步转存）→ 自动整理入库。
//
// 认证：在 hdhive.com 站内申请 OpenAPI 应用（审核通过）获得 API Key，
// 调用时放 X-API-Key 请求头。接口按天限额，超限返回 429（带 Retry-After）。
// 转存仅支持 115 分享链接（StrmHub 的媒体库建立在 115 上）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"strmhub/internal/model"
)

const hdhiveDefaultBase = "https://hdhive.com"

// regexpPureDigits 纯数字输入视为 TMDB ID 直查
var regexpPureDigits = regexp.MustCompile(`^\d{1,8}$`)

// hdhiveCfg 影巢配置（setting key "hdhive"）
type hdhiveCfg struct {
	APIKey      string `json:"api_key"`      // OpenAPI 应用密钥
	BaseURL     string `json:"base_url"`     // 默认 https://hdhive.com（站点迁移镜像时可改）
	AllowPoints bool   `json:"allow_points"` // 免费资源解锁失败时是否允许消耗积分
	Organize    bool   `json:"organize"`     // 转存后自动整理入库（默认开）
}

var (
	hdhiveCfgMu sync.Mutex
	hdhiveCfgV  *hdhiveCfg
	hdhiveCfgAt time.Time
)

// loadHdhiveCfg 读取配置（10 秒缓存）
func loadHdhiveCfg() *hdhiveCfg {
	hdhiveCfgMu.Lock()
	defer hdhiveCfgMu.Unlock()
	if hdhiveCfgV != nil && time.Since(hdhiveCfgAt) < 10*time.Second {
		return hdhiveCfgV
	}
	cfg := &hdhiveCfg{BaseURL: hdhiveDefaultBase, Organize: true}
	if v := settingValueCompat("hdhive"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = hdhiveDefaultBase
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	hdhiveCfgV = cfg
	hdhiveCfgAt = time.Now()
	return cfg
}

func invalidateHdhiveCfg() {
	hdhiveCfgMu.Lock()
	hdhiveCfgV = nil
	hdhiveCfgMu.Unlock()
}

// hdhiveCall 调用影巢 OpenAPI。响应壳：{success, code, message, data}
// 成功时把 data 写入 out；失败时返回带原因的错误（429 附带重试提示）。
func hdhiveCall(cfg *hdhiveCfg, method, path string, body any, out any) error {
	if cfg == nil || cfg.APIKey == "" {
		return fmt.Errorf("未配置影巢 API Key（影视转存 → 影巢 → 配置）")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("连接影巢失败: %s", sanitizeWecomErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode == http.StatusTooManyRequests {
		ra := resp.Header.Get("Retry-After")
		if ra != "" {
			return fmt.Errorf("影巢接口今日额度已用完（Retry-After %s 秒）", ra)
		}
		return fmt.Errorf("影巢接口今日额度已用完，请明天再试")
	}
	var env struct {
		Success bool            `json:"success"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return fmt.Errorf("影巢响应解析失败（HTTP %d）: %s", resp.StatusCode, truncateStr(string(raw), 120))
	}
	if resp.StatusCode >= 400 || !env.Success {
		msg := env.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("影巢接口错误: %s", msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("影巢数据解析失败: %v", err)
		}
	}
	return nil
}

// ==================== HTTP 处理器 ====================

// HdhiveGetConfig GET /hdhive/config
func (h *Handler) HdhiveGetConfig(c *gin.Context) {
	cfg := loadHdhiveCfg()
	c.JSON(http.StatusOK, gin.H{
		"api_key":      cfg.APIKey,
		"base_url":     cfg.BaseURL,
		"allow_points": cfg.AllowPoints,
		"organize":     cfg.Organize,
		"configured":   cfg.APIKey != "",
	})
}

// HdhiveSaveConfig POST /hdhive/config {api_key, base_url, allow_points, organize}
func (h *Handler) HdhiveSaveConfig(c *gin.Context) {
	var req hdhiveCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.BaseURL == "" {
		req.BaseURL = hdhiveDefaultBase
	}
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting("hdhive", string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	invalidateHdhiveCfg()
	log.Printf("[影巢] ✓ 配置已保存")
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// HdhiveTest POST /hdhive/test → ping + 配额
func (h *Handler) HdhiveTest(c *gin.Context) {
	cfg := loadHdhiveCfg()
	if cfg.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写 API Key 并保存"})
		return
	}
	var ping struct {
		Version   string `json:"version"`
		Timestamp string `json:"timestamp"`
	}
	if err := hdhiveCall(cfg, http.MethodGet, "/api/open/ping", nil, &ping); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var quota any
	if err := hdhiveCall(cfg, http.MethodGet, "/api/open/quota", nil, &quota); err != nil {
		// ping 通过但配额接口失败不阻断（老版本可能没有该接口）
		log.Printf("[影巢] ○ 配额查询失败（忽略）: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"message": "连接成功", "ping": ping, "quota": quota})
}

// HdhiveTmdbSearch GET /hdhive/tmdb/search?query=xxx
// 借 TMDB multi-search 把影视名称转成条目（movie/tv），供选片后查影巢资源
func (h *Handler) HdhiveTmdbSearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("query"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入影视名称"})
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error() + "（先在系统配置完成 TMDB 设置）"})
		return
	}
	// 纯数字输入 → 按 TMDB ID 直查详情（先电影后剧集，不消耗影巢配额）
	if regexpPureDigits.MatchString(q) {
		for _, kind := range []string{"movie", "tv"} {
			body, err := tc.get("/"+kind+"/"+q, nil)
			if err != nil {
				continue
			}
			var d struct {
				ID          int     `json:"id"`
				Title       string  `json:"title"`
				Name        string  `json:"name"`
				ReleaseDate string  `json:"release_date"`
				FirstAir    string  `json:"first_air_date"`
				PosterPath  string  `json:"poster_path"`
				VoteAverage float64 `json:"vote_average"`
			}
			if json.Unmarshal(body, &d) != nil || d.ID == 0 {
				continue
			}
			title := d.Title
			date := d.ReleaseDate
			if title == "" {
				title = d.Name
			}
			if date == "" {
				date = d.FirstAir
			}
			year := ""
			if len(date) >= 4 {
				year = date[:4]
			}
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{
				"id": d.ID, "media_type": kind, "title": title,
				"year": year, "poster": d.PosterPath, "vote": d.VoteAverage,
			}}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{}, "hint": "未找到该 TMDB ID 对应的影视条目"})
		return
	}
	body, err := tc.get("/search/multi", map[string]string{"query": q, "include_adult": "false"})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 搜索失败: " + err.Error()})
		return
	}
	var result struct {
		Results []struct {
			ID           int     `json:"id"`
			MediaType    string  `json:"media_type"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			OriginalName string  `json:"original_name"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			PosterPath   string  `json:"poster_path"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &result) != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 响应解析失败"})
		return
	}
	items := make([]gin.H, 0, len(result.Results))
	for _, r := range result.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue // multi-search 会混入 person
		}
		title := r.Title
		date := r.ReleaseDate
		if title == "" {
			title = r.Name
		}
		if date == "" {
			date = r.FirstAirDate
		}
		year := ""
		if len(date) >= 4 {
			year = date[:4]
		}
		items = append(items, gin.H{
			"id":         r.ID,
			"media_type": r.MediaType,
			"title":      title,
			"year":       year,
			"poster":     r.PosterPath,
			"vote":       r.VoteAverage,
		})
		if len(items) >= 12 {
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// HdhiveTmdbImg GET /hdhive/tmdbimg?path=/xx.jpg&size=w92
// 海报代理：走 TMDB 配置里的代理设置拉图（国内直连 image.tmdb.org 常不通）
func (h *Handler) HdhiveTmdbImg(c *gin.Context) {
	p := c.Query("path")
	if !strings.HasPrefix(p, "/") {
		c.Status(http.StatusBadRequest)
		return
	}
	size := c.Query("size")
	if size == "" {
		size = "w92"
	}
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil || cfg.ImageApiUrl == "" {
		c.Status(http.StatusNotFound)
		return
	}
	base := strings.TrimRight(cfg.ImageApiUrl, "/")
	if !strings.HasSuffix(base, "/t/p") {
		base += "/t/p" // 配置里一般只填到域名，海报尺寸挂在 /t/p 下
	}
	u := base + "/" + size + p
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	client := &http.Client{Timeout: 15 * time.Second}
	// 优先 TMDB 配置的代理，其次全局代理（与海报抓取同策略）
	proxyURL := getProxyURL()
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxyURL = cfg.ProxyUrl
	}
	if proxyURL != "" {
		if pu, perr := parseProxyURL(proxyURL); perr == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.DataFromReader(http.StatusOK, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}

// hdhiveResource 影巢资源条目（字段宽松解析：缺省不报错）
type hdhiveResource struct {	Slug           string `json:"slug"`
	Title          string `json:"title"`
	Size           int64  `json:"size"`
	Resolution     string `json:"resolution"`
	Source         string `json:"source"`
	Quality        string `json:"quality"`
	Subtitle       string `json:"subtitle"`
	SubtitleTeam   string `json:"subtitle_team"`
	Remark         string `json:"remark"`
	Official       bool   `json:"official"`
	ValidateStatus string `json:"validate_status"`
	PanType        string `json:"pan_type"`
	UnlockPoints   int    `json:"unlock_points"`
	IsFree         bool   `json:"is_free"`
	IsUnlocked     bool   `json:"is_unlocked"`
	CreatedAt      string `json:"created_at"`
	Author         string `json:"author_username"`
}

// HdhiveResources GET /hdhive/resources?media_type=movie&tmdb_id=550
func (h *Handler) HdhiveResources(c *gin.Context) {
	cfg := loadHdhiveCfg()
	if cfg.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置影巢 API Key"})
		return
	}
	mt := c.Query("media_type")
	if mt != "movie" && mt != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type 须为 movie 或 tv"})
		return
	}
	id := strings.TrimSpace(c.Query("tmdb_id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 tmdb_id"})
		return
	}
	var raw json.RawMessage
	if err := hdhiveCall(cfg, http.MethodGet, "/api/open/resources/"+mt+"/"+url.PathEscape(id), nil, &raw); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// 上游 data 可能是 {list:[...], total} 也可能直接是数组，两种都兼容
	var data struct {
		List  []hdhiveResource `json:"list"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.List == nil {
		var arr []hdhiveResource
		if err2 := json.Unmarshal(raw, &arr); err2 == nil {
			data.List = arr
			data.Total = len(arr)
		}
	}
	if data.List == nil {
		data.List = []hdhiveResource{}
	}
	c.JSON(http.StatusOK, gin.H{"data": data.List, "total": data.Total})
}

// hdhiveUnlock 解锁资源，返回分享链接与访问码
func hdhiveUnlock(cfg *hdhiveCfg, slug string, allowPoints bool) (panType, link, accessCode string, alreadyOwned bool, err error) {
	var data struct {
		URL          string `json:"url"`
		AccessCode   string `json:"access_code"`
		FullURL      string `json:"full_url"`
		PanType      string `json:"pan_type"`
		AlreadyOwned bool   `json:"already_owned"`
	}
	body := map[string]any{"slug": slug}
	if allowPoints {
		body["allow_points"] = true
	}
	if err = hdhiveCall(cfg, http.MethodPost, "/api/open/resources/unlock", body, &data); err != nil {
		return
	}
	link = data.FullURL
	if link == "" {
		link = data.URL
	}
	if link == "" {
		err = fmt.Errorf("解锁响应缺少分享链接（上游数据异常，可稍后重试）")
		return
	}
	if data.AccessCode != "" {
		accessCode = data.AccessCode
	} else {
		// 访问码可能内嵌在链接里（?password=xxxx / #xxxx）
		if m := reSharePass.FindStringSubmatch(link); m != nil {
			accessCode = m[1]
		}
	}
	return data.PanType, link, accessCode, data.AlreadyOwned, nil
}

// HdhiveTransfer POST /hdhive/transfer {slug, organize?, target_cid?}
// 解锁 → 校验是 115 分享 → 复用 Cookie 通道转存（含可选自动整理）
func (h *Handler) HdhiveTransfer(c *gin.Context) {
	var req struct {
		Slug      string `json:"slug"`
		Organize  *bool  `json:"organize"`
		TargetCID string `json:"target_cid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少资源标识（slug）"})
		return
	}
	cfg := loadHdhiveCfg()
	organize := cfg.Organize
	if req.Organize != nil {
		organize = *req.Organize
	}
	panType, link, code, _, err := hdhiveUnlock(cfg, strings.TrimSpace(req.Slug), cfg.AllowPoints)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[影巢] ▣ 解锁成功（%s）: %s", panType, truncateStr(link, 90))
	if !is115ShareLink(link) {
		c.JSON(http.StatusOK, gin.H{
			"transferred": false,
			"url":         link,
			"access_code": code,
			"pan_type":    panType,
			"message":     "该资源是 " + strings.ToUpper(panType) + " 分享，暂不支持自动转存，链接已给出可手动保存",
		})
		return
	}
	msg, ok, fail, err := h.shareReceiveCore(link, code, req.TargetCID, organize)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "url": link, "access_code": code})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"transferred": true,
		"success":     ok,
		"failed":      fail,
		"message":     msg,
		"url":         link,
		"access_code": code,
		"note":        "转存完成，增量同步将自动生成 STRM（开启整理时）",
	})
}
