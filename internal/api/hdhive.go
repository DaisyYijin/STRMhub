package api

// ==================== HDHive（影巢）资源站接入 ====================
//
// 接入模型（影巢 2026-06 起新规）：OpenAPI 应用 + OAuth 用户授权
//   - 站内申请应用，审核通过后获得 Client ID 与应用 Secret
//   - 应用 Secret 调用 /api/open/* 时放 X-API-Key 头
//   - 用户级操作（查资源/解锁）走 OAuth 授权码：浏览器跳转 /oauth/authorize
//     授权后回调 StrmHub，换取 access_token（可 refresh）
//
// 本页当前提供：应用配置、连接测试、账号授权、授权后拉取账号基本信息。
// 资源查询/解锁/115 转存的后端链路保留（/hdhive/resources、/hdhive/transfer），
// 等应用审核通过后按资源站页面逐步放开。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const hdhiveDefaultBase = "https://hdhive.com"

// hdhiveCfg 影巢配置（setting key "hdhive"）
type hdhiveCfg struct {
	ClientID     string `json:"client_id"`      // OpenAPI 应用 Client ID
	APIKey       string `json:"api_key"`        // 应用 Secret（X-API-Key 头）
	BaseURL      string `json:"base_url"`       // 默认 https://hdhive.com
	AllowPoints  bool   `json:"allow_points"`   // 免费资源解锁失败时是否允许消耗积分
	Organize     bool   `json:"organize"`       // 转存后自动整理入库（默认开）
	AccessToken  string `json:"access_token"`   // OAuth 用户令牌
	RefreshToken string `json:"refresh_token"`  // 刷新令牌
	TokenExp     int64  `json:"token_exp"`      // access_token 过期时间（unix 秒，0=未知）
	AuthorizedAt string `json:"authorized_at"`  // 授权时间（展示用）
	UserJSON     string `json:"user_json"`      // 授权时拉到的账号基本信息（原样缓存）
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

// saveHdhiveCfg 写回配置并失效缓存（与 settingValueCompat 同源：注入的 Config 写 YAML）
func saveHdhiveCfg(cfg *hdhiveCfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("hdhive", string(b)); err != nil {
		return err
	}
	invalidateHdhiveCfg()
	return nil
}

func invalidateHdhiveCfg() {
	hdhiveCfgMu.Lock()
	hdhiveCfgV = nil
	hdhiveCfgMu.Unlock()
}

// hdhiveCall 调用影巢 OpenAPI（应用级：X-API-Key 鉴权）。
// 响应壳：{success, code, message, data}；withToken 时附带用户 Bearer 令牌。
func hdhiveCall(cfg *hdhiveCfg, method, path string, body any, out any, withToken bool) error {
	if cfg == nil || cfg.APIKey == "" {
		return fmt.Errorf("未配置影巢应用 Secret（影视转存 → 影巢 → 接入配置）")
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
	if withToken {
		if cfg.AccessToken == "" {
			return fmt.Errorf("影巢账号未授权，请先在「影视转存 → 影巢」完成授权")
		}
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	}

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

// ==================== OAuth 授权码流程 ====================

// hdhiveOAuthStates 一次性 state（CSRF 防护），10 分钟有效
var hdhiveOAuthStates = struct {
	sync.Mutex
	m map[string]int64
}{m: map[string]int64{}}

func hdhiveStatePut() string {
	b := make([]byte, 16)
	rand.Read(b)
	s := hex.EncodeToString(b)
	hdhiveOAuthStates.Lock()
	// 顺手清理过期项
	for k, exp := range hdhiveOAuthStates.m {
		if time.Now().Unix() > exp {
			delete(hdhiveOAuthStates.m, k)
		}
	}
	hdhiveOAuthStates.m[s] = time.Now().Add(10 * time.Minute).Unix()
	hdhiveOAuthStates.Unlock()
	return s
}

func hdhiveStateTake(s string) bool {
	if s == "" {
		return false
	}
	hdhiveOAuthStates.Lock()
	defer hdhiveOAuthStates.Unlock()
	exp, ok := hdhiveOAuthStates.m[s]
	if !ok || time.Now().Unix() > exp {
		delete(hdhiveOAuthStates.m, s)
		return false
	}
	delete(hdhiveOAuthStates.m, s) // 单次有效
	return true
}

// hdhiveRedirectURI 从请求推断回调地址（反代场景读 X-Forwarded-Proto）
func hdhiveRedirectURI(c *gin.Context) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + c.Request.Host + "/api/hdhive/oauth/callback"
}

// HdhiveOAuthStart POST /hdhive/oauth/start → 返回影巢授权页跳转地址
func (h *Handler) HdhiveOAuthStart(c *gin.Context) {
	cfg := loadHdhiveCfg()
	if cfg.ClientID == "" || cfg.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写并保存 Client ID 与应用 Secret"})
		return
	}
	redirectURI := hdhiveRedirectURI(c)
	state := hdhiveStatePut()
	authURL := fmt.Sprintf("%s/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&state=%s",
		cfg.BaseURL, url.QueryEscape(cfg.ClientID), url.QueryEscape(redirectURI), state)
	log.Printf("[影巢] ▶ 发起授权跳转（回调 %s）", redirectURI)
	c.JSON(http.StatusOK, gin.H{"url": authURL, "redirect_uri": redirectURI})
}

// HdhiveOAuthCallback GET /hdhive/oauth/callback（公开路由：授权跳回时不带登录态）
// 换取 token → 拉账号信息 → 存库 → 302 回管理页
func (h *Handler) HdhiveOAuthCallback(c *gin.Context) {
	back := func(ok bool, msg string) {
		q := url.Values{}
		if ok {
			q.Set("hdhive_auth", "1")
		} else {
			q.Set("hdhive_auth", "0")
			q.Set("hdhive_msg", msg)
		}
		c.Redirect(http.StatusFound, "/media-transfer?"+q.Encode())
	}
	if c.Query("error") != "" {
		back(false, "影巢返回授权失败: "+c.Query("error"))
		return
	}
	code := c.Query("code")
	if !hdhiveStateTake(c.Query("state")) {
		back(false, "授权状态校验失败（state 已过期，请重新发起授权）")
		return
	}
	if code == "" {
		back(false, "缺少授权码")
		return
	}
	cfg := loadHdhiveCfg()
	if cfg.ClientID == "" || cfg.APIKey == "" {
		back(false, "应用配置缺失（Client ID / Secret 未保存）")
		return
	}
	redirectURI := hdhiveRedirectURI(c)

	// 换取 access_token（响应壳可能是 {data:{...}} 嵌套或平铺，两种都兼容）
	tokBody, _ := json.Marshal(map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     cfg.ClientID,
		"client_secret": cfg.APIKey,
		"code":          code,
		"redirect_uri":  redirectURI,
	})
	req, _ := http.NewRequest(http.MethodPost, cfg.BaseURL+"/api/open/oauth/token", bytes.NewReader(tokBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		back(false, "换取令牌失败: "+sanitizeWecomErr(err))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Data *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &tr) != nil {
		back(false, "令牌响应解析失败: "+truncateStr(string(raw), 100))
		return
	}
	if tr.Data != nil {
		tr.AccessToken = tr.Data.AccessToken
		tr.RefreshToken = tr.Data.RefreshToken
		tr.ExpiresIn = tr.Data.ExpiresIn
	}
	if tr.AccessToken == "" {
		msg := tr.Message
		if msg == "" {
			msg = truncateStr(string(raw), 100)
		}
		back(false, "换取令牌被拒: "+msg)
		return
	}

	cfg.AccessToken = tr.AccessToken
	cfg.RefreshToken = tr.RefreshToken
	if tr.ExpiresIn > 0 {
		cfg.TokenExp = time.Now().Add(time.Duration(tr.ExpiresIn-120) * time.Second).Unix()
	} else {
		cfg.TokenExp = 0
	}
	cfg.AuthorizedAt = time.Now().Format("2006-01-02 15:04")

	// 拉账号基本信息（失败不阻断授权：信息页可手动刷新重拉）
	if u, err := hdhiveFetchUser(cfg); err != nil {
		log.Printf("[影巢] ○ 授权成功但拉取账号信息失败: %v", err)
	} else {
		b, _ := json.Marshal(u)
		cfg.UserJSON = string(b)
	}
	if err := saveHdhiveCfg(cfg); err != nil {
		back(false, "授权信息保存失败: "+err.Error())
		return
	}
	log.Printf("[影巢] ✓ 授权成功")
	back(true, "")
}

// hdhiveRefreshToken 用 refresh_token 续期（写回配置）
func hdhiveRefreshToken(cfg *hdhiveCfg) error {
	if cfg.RefreshToken == "" {
		return fmt.Errorf("无刷新令牌，请重新授权")
	}
	tokBody, _ := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     cfg.ClientID,
		"client_secret": cfg.APIKey,
		"refresh_token": cfg.RefreshToken,
	})
	req, _ := http.NewRequest(http.MethodPost, cfg.BaseURL+"/api/open/oauth/token", bytes.NewReader(tokBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("刷新令牌失败: %s", sanitizeWecomErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Data *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &tr) != nil {
		return fmt.Errorf("刷新令牌响应解析失败: %s", truncateStr(string(raw), 100))
	}
	if tr.Data != nil {
		tr.AccessToken = tr.Data.AccessToken
		tr.RefreshToken = tr.Data.RefreshToken
		tr.ExpiresIn = tr.Data.ExpiresIn
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("刷新令牌被拒: %s", tr.Message)
	}
	cfg.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		cfg.RefreshToken = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		cfg.TokenExp = time.Now().Add(time.Duration(tr.ExpiresIn-120) * time.Second).Unix()
	}
	return saveHdhiveCfg(cfg)
}

// hdhiveFetchUser 拉账号基本信息（Bearer 用户令牌；/user 不在时退化 /user/info）
func hdhiveFetchUser(cfg *hdhiveCfg) (map[string]any, error) {
	var u map[string]any
	err := hdhiveCall(cfg, http.MethodGet, "/api/open/user", nil, &u, true)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "不存在") {
			u = map[string]any{}
			if err2 := hdhiveCall(cfg, http.MethodGet, "/api/open/user/info", nil, &u, true); err2 == nil {
				return u, nil
			}
		}
		return nil, err
	}
	return u, nil
}

// HdhiveUser GET /hdhive/user → 授权状态 + 账号基本信息（过期自动刷新令牌）
func (h *Handler) HdhiveUser(c *gin.Context) {
	cfg := loadHdhiveCfg()
	if cfg.AccessToken == "" {
		c.JSON(http.StatusOK, gin.H{"authorized": false, "configured": cfg.ClientID != "" && cfg.APIKey != ""})
		return
	}
	// 令牌将过期且可刷新 → 先续期
	if cfg.TokenExp > 0 && time.Now().Add(60*time.Second).Unix() > cfg.TokenExp {
		if err := hdhiveRefreshToken(cfg); err != nil {
			log.Printf("[影巢] ○ 令牌续期失败: %v", err)
		}
	}
	user := map[string]any{}
	if cfg.UserJSON != "" {
		json.Unmarshal([]byte(cfg.UserJSON), &user)
	}
	// 缓存为空（授权时拉取失败）→ 现场重拉一次
	if len(user) == 0 {
		if u, err := hdhiveFetchUser(cfg); err == nil {
			user = u
			b, _ := json.Marshal(u)
			cfg.UserJSON = string(b)
			_ = saveHdhiveCfg(cfg)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"authorized":    true,
		"configured":    cfg.ClientID != "" && cfg.APIKey != "",
		"authorized_at": cfg.AuthorizedAt,
		"user":          user,
	})
}

// HdhiveOAuthRevoke POST /hdhive/oauth/revoke → 清除授权
func (h *Handler) HdhiveOAuthRevoke(c *gin.Context) {
	cfg := loadHdhiveCfg()
	cfg.AccessToken, cfg.RefreshToken, cfg.TokenExp = "", "", 0
	cfg.AuthorizedAt, cfg.UserJSON = "", ""
	if err := saveHdhiveCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清除失败: " + err.Error()})
		return
	}
	log.Printf("[影巢] ○ 已取消授权")
	c.JSON(http.StatusOK, gin.H{"message": "已取消授权"})
}

// ==================== 配置与连接测试 ====================

// HdhiveGetConfig GET /hdhive/config
func (h *Handler) HdhiveGetConfig(c *gin.Context) {
	cfg := loadHdhiveCfg()
	c.JSON(http.StatusOK, gin.H{
		"client_id":     cfg.ClientID,
		"api_key":       cfg.APIKey,
		"base_url":      cfg.BaseURL,
		"allow_points":  cfg.AllowPoints,
		"organize":      cfg.Organize,
		"configured":    cfg.ClientID != "" && cfg.APIKey != "",
		"authorized":    cfg.AccessToken != "",
		"authorized_at": cfg.AuthorizedAt,
	})
}

// HdhiveSaveConfig POST /hdhive/config {client_id, api_key}
// （allow_points/organize 暂无 UI 控制，保留存量值；资源链路放开时一并恢复）
func (h *Handler) HdhiveSaveConfig(c *gin.Context) {
	var req struct {
		ClientID string `json:"client_id"`
		APIKey   string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadHdhiveCfg()
	cfg.ClientID = strings.TrimSpace(req.ClientID)
	cfg.APIKey = strings.TrimSpace(req.APIKey)
	if err := saveHdhiveCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[影巢] ✓ 接入配置已保存")
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// HdhiveTest POST /hdhive/test → ping + 配额（应用级，不需要用户授权）
func (h *Handler) HdhiveTest(c *gin.Context) {
	cfg := loadHdhiveCfg()
	if cfg.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写应用 Secret 并保存"})
		return
	}
	var ping struct {
		Version   string `json:"version"`
		Timestamp string `json:"timestamp"`
	}
	if err := hdhiveCall(cfg, http.MethodGet, "/api/open/ping", nil, &ping, false); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var quota any
	if err := hdhiveCall(cfg, http.MethodGet, "/api/open/quota", nil, &quota, false); err != nil {
		// ping 通过但配额接口失败不阻断（老版本可能没有该接口）
		log.Printf("[影巢] ○ 配额查询失败（忽略）: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"message": "连接成功", "ping": ping, "quota": quota})
}
