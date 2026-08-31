package api

// ==================== 观影（GuanYing）资源站接入 ====================
//
// 观影是公开的网盘影视资源索引站（电影/剧集/动漫，条目挂网盘分享链接）。
// 无需任何凭据：后端代理搜索与详情接口，从响应里提取网盘链接并按域名
// 分类；115 链接可一键转存（复用 share.go 的 Cookie 通道），其他网盘
// 复制链接手动转存。
//
// 站点常换域名（gying.in 已失效，现行 观影.com），所以 base_url 做成
// 可配置（影视转存 → 观影 → 站点设置）。接口无官方文档，实现按
// 「多候选端点探测 + 字段无关提取」设计：不管上游字段叫什么，只要
// 响应 JSON 里有网盘链接就能拿到。

import (
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
)

const gyDefaultBase = "https://www.xn--wcv59z.com"

// gyCfg 观影配置（setting key "guanying"）
type gyCfg struct {
	BaseURL string `json:"base_url"`
}

var (
	gyCfgMu sync.Mutex
	gyCfgV  *gyCfg
	gyCfgAt time.Time
)

func loadGyCfg() *gyCfg {
	gyCfgMu.Lock()
	defer gyCfgMu.Unlock()
	if gyCfgV != nil && time.Since(gyCfgAt) < 10*time.Second {
		return gyCfgV
	}
	cfg := &gyCfg{BaseURL: gyDefaultBase}
	if v := settingValueCompat("guanying"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" || !strings.HasPrefix(cfg.BaseURL, "http") {
		cfg.BaseURL = gyDefaultBase
	}
	gyCfgV = cfg
	gyCfgAt = time.Now()
	return cfg
}

func saveGyCfg(cfg *gyCfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("guanying", string(b)); err != nil {
		return err
	}
	gyCfgMu.Lock()
	gyCfgV = nil
	gyCfgMu.Unlock()
	return nil
}

// gyGet 带浏览器头的 GET（站点拦截非浏览器 UA）
func gyGet(base, pathWithQuery string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, base+pathWithQuery, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

// ==================== 链接提取（字段无关） ====================

// gyLink 一条网盘链接
type gyLink struct {
	URL  string `json:"url"`
	Code string `json:"code"` // 提取码/访问码（可能为空）
	Pan  string `json:"pan"`  // 115/quark/baidu/xunlei/uc/ali/123/tianyi/magnet
}

var (
	gyPanRules = []struct {
		pattern *regexp.Regexp
		pan     string
	}{
		{regexp.MustCompile(`(?:115\.com|115cdn\.com|anxia\.com)/s/`), "115"},
		{regexp.MustCompile(`pan\.quark\.cn/s/`), "quark"},
		{regexp.MustCompile(`pan\.baidu\.com/s/`), "baidu"},
		{regexp.MustCompile(`pan\.xunlei\.com/s/`), "xunlei"},
		{regexp.MustCompile(`drive\.uc\.cn/s/`), "uc"},
		{regexp.MustCompile(`(?:alipan|aliyundrive)\.com/s/`), "ali"},
		{regexp.MustCompile(`123(?:pan|684|865|912)\.(?:com|cn)/s/`), "123"},
		{regexp.MustCompile(`cloud\.189\.cn/[tw]/`), "tianyi"},
		{regexp.MustCompile(`^magnet:\?xt=urn:btih:`), "magnet"},
	}
	reGyCode   = regexp.MustCompile(`(?:提取码|访问码|密码|pwd|pass(?:word)?|code)[=:：\s]*([A-Za-z0-9]{4})`)
	reGyYear   = regexp.MustCompile(`(19|20)\d{2}`)
	reHTMLText = regexp.MustCompile(`<[^>]+>`)
)

// gyDetectPan 按域名识别网盘类型，非网盘链接返回空
func gyDetectPan(s string) string {
	for _, r := range gyPanRules {
		if r.pattern.MatchString(s) {
			return r.pan
		}
	}
	return ""
}

// gyWalkJSON 递归遍历 JSON 树：收集所有网盘链接（同一字符串里的提取码一并带上）
func gyWalkJSON(v any, links *[]gyLink, seen map[string]bool) {
	switch t := v.(type) {
	case string:
		text := reHTMLText.ReplaceAllString(t, " ") // 剥掉混入的 HTML 标签后再匹配
		// 一个字符串可能包含多条链接（空格/换行分隔），逐条找
		for _, seg := range strings.FieldsFunc(text, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\t' || r == '"' || r == '\'' || r == '<' || r == '>'
		}) {
			if pan := gyDetectPan(seg); pan != "" {
				u := strings.TrimRight(seg, ",.;，。；）)")
				if u != "" && !seen[u] {
					seen[u] = true
					link := gyLink{URL: u, Pan: pan}
					if m := reGyCode.FindStringSubmatch(text); m != nil {
						link.Code = m[1]
					}
					*links = append(*links, link)
				}
			}
		}
	case []any:
		for _, e := range t {
			gyWalkJSON(e, links, seen)
		}
	case map[string]any:
		for _, e := range t {
			gyWalkJSON(e, links, seen)
		}
	}
}

// gyPickString 从对象里按候选键名取第一个非空字符串
func gyPickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// gyFindArray 在响应里找条目数组：data 是数组就用，否则找 data.list / data.items
func gyFindArray(raw []byte) []any {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	if arr, ok := root["data"].([]any); ok {
		// data 直接是数组（空数组也原样返回）
		if len(arr) == 0 {
			return arr
		}
		if _, isObj := arr[0].(map[string]any); isObj {
			return arr
		}
	}
	if data, ok := root["data"].(map[string]any); ok {
		for _, k := range []string{"list", "items", "rows", "records"} {
			if arr, ok := data[k].([]any); ok {
				return arr
			}
		}
	}
	return nil
}

// ==================== HTTP 处理器 ====================

// GyGetConfig GET /guanying/config
func (h *Handler) GyGetConfig(c *gin.Context) {
	cfg := loadGyCfg()
	c.JSON(http.StatusOK, gin.H{"base_url": cfg.BaseURL, "default_base": gyDefaultBase})
}

// GySaveConfig POST /guanying/config {base_url}
func (h *Handler) GySaveConfig(c *gin.Context) {
	var req gyCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.BaseURL == "" {
		req.BaseURL = gyDefaultBase
	}
	if !strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "站点地址需以 http(s):// 开头"})
		return
	}
	if err := saveGyCfg(&req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ✓ 站点地址已保存: %s", req.BaseURL)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// GySearch GET /guanying/search?query=xxx
func (h *Handler) GySearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("query"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入影视名称"})
		return
	}
	cfg := loadGyCfg()
	raw, err := gyGet(cfg.BaseURL, "/api/v1/search?keyword="+url.QueryEscape(q)+"&page=1")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "连接观影失败: " + err.Error() + "（站点换域名时请在站点设置里更新地址）"})
		return
	}
	arr := gyFindArray(raw)
	items := make([]gin.H, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		title := gyPickString(m, "title", "name", "vod_name", "video_name")
		if title == "" {
			continue
		}
		year := gyPickString(m, "year", "date", "release_date", "year_title")
		if ym := reGyYear.FindString(year); ym != "" {
			year = ym
		} else {
			year = ""
		}
		id := ""
		switch v := m["id"].(type) {
		case string:
			id = v
		case float64:
			id = fmt.Sprintf("%.0f", v)
		}
		if id == "" {
			id = gyPickString(m, "uuid", "slug", "vod_id", "_id")
		}
		if id == "" {
			continue
		}
		items = append(items, gin.H{
			"id":     id,
			"title":  title,
			"year":   year,
			"poster": gyPickString(m, "poster", "pic", "cover", "image", "img"),
			"type":   gyPickString(m, "type", "category", "channel", "type_name"),
		})
		if len(items) >= 24 {
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// gyDetailPaths 详情接口候选（无官方文档，按常见命名探测，命中即用）
var gyDetailPaths = []string{"/api/v1/detail", "/api/v1/resource", "/api/v1/video", "/api/v1/item", "/api/v1/info"}

// GyResources GET /guanying/resources?id=xxx
func (h *Handler) GyResources(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少条目 ID"})
		return
	}
	cfg := loadGyCfg()
	var lastErr string
	for _, p := range gyDetailPaths {
		raw, err := gyGet(cfg.BaseURL, p+"?id="+url.QueryEscape(id))
		if err != nil {
			lastErr = p + ": " + err.Error()
			continue
		}
		var links []gyLink
		seen := map[string]bool{}
		gyWalkJSON(raw, &links, seen)
		if len(links) > 0 {
			// 标题：顺手从响应里挑一个像标题的字段
			var obj map[string]any
			_ = json.Unmarshal(raw, &obj)
			title := gyFindTitle(obj, 0)
			log.Printf("[观影] ▣ 详情命中 %s：提取到 %d 条链接（%s）", p, len(links), truncateStr(title, 40))
			c.JSON(http.StatusOK, gin.H{"data": links, "title": title, "endpoint": p})
			return
		}
		lastErr = p + ": 响应中无网盘链接"
	}
	log.Printf("[观影] ✗ 详情接口未命中（id=%s）：%s", id, lastErr)
	c.JSON(http.StatusBadGateway, gin.H{"error": "详情接口未命中（站点可能已改版，请把这条日志发给开发者）: " + lastErr})
}

// gyFindTitle 递归找第一个像标题的字符串值（限制深度防爆栈）
func gyFindTitle(v any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		if s := gyPickString(t, "title", "name", "vod_name", "video_name"); s != "" {
			return s
		}
		for _, e := range t {
			if s := gyFindTitle(e, depth+1); s != "" {
				return s
			}
		}
	case []any:
		for _, e := range t {
			if s := gyFindTitle(e, depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

// GyTransfer POST /guanying/transfer {url, code, target_cid?}
// 仅 115 分享可自动转存（复用 Cookie 通道 + 自动整理闭环）
func (h *Handler) GyTransfer(c *gin.Context) {
	var req struct {
		URL       string `json:"url"`
		Code      string `json:"code"`
		TargetCID string `json:"target_cid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少链接"})
		return
	}
	if !is115ShareLink(req.URL) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 115 分享链接的自动转存，其他网盘请复制链接手动转存"})
		return
	}
	msg, ok, fail, err := h.shareReceiveCore(req.URL, req.Code, req.TargetCID, true)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if ok == 0 && fail == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "分享为空", "count": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "count": ok, "failed": fail,
		"note": "转存完成，增量同步将自动生成 STRM"})
}
