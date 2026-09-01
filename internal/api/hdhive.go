package api

// ==================== HDHive（影巢）资源站接入 ====================
//
// 认证模型（与 emby-pulse Pro / MoviePilot agentresourceofficer 的实现互证）：
//   - X-API-Key：应用 Secret，或个人 API Key（hdhive.com「个人设置 → API 管理」
//     创建，自带用户身份）
//   - Bearer 用户令牌：OAuth 授权码换取（可 refresh）；业务接口（搜资源/解锁）
//     需要，401/403 自动续期并重试一次
//
// 资源链路：TMDB 条目 → GET /api/open/resources/{movie|tv}/{tmdb_id}
// → POST /api/open/resources/unlock {slug} → 网盘分享链接 → 115 自动转存
// （分享四步转存）或磁力/ed2k/HTTP 走离线下载，完成后自动整理入库。

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const hdhiveDefaultBase = "https://hdhive.com"

// 内置应用凭据：项目作者在影巢申请的 OpenAPI 应用，构建镜像时经
// GitHub Actions Secret 注入（仓库源码不落盘），让所有部署开箱即可
// 跳转授权。HDHIVE_REDIRECT_RELAY 是静态中转页地址——影巢回调白名单
// 只认这一个固定地址，中转页再把授权码转发到各部署自己的回调。
func hdhiveBuiltin() (clientID, secret, relay string) {
	return os.Getenv("HDHIVE_CLIENT_ID"), os.Getenv("HDHIVE_SECRET"), os.Getenv("HDHIVE_REDIRECT_RELAY")
}

// resolveCreds 返回实际生效的凭据：用户自备优先，回落内置应用。
// source: custom=用户自备 / builtin=镜像内置 / ""=两者皆无
func (cfg *hdhiveCfg) resolveCreds() (clientID, secret, source, relay string) {
	if id, sk, rl := hdhiveBuiltin(); id != "" && sk != "" {
		clientID, secret, source, relay = id, sk, "builtin", rl
	}
	if cfg.ClientID != "" && cfg.APIKey != "" {
		return cfg.ClientID, cfg.APIKey, "custom", ""
	}
	return
}

// hdhiveCfg 影巢配置（setting key "hdhive"）
type hdhiveCfg struct {
	ClientID     string `json:"client_id"`      // OpenAPI 应用 Client ID
	APIKey       string `json:"api_key"`        // 应用 Secret 或个人 API Key（X-API-Key 头）
	BaseURL      string `json:"base_url"`       // 默认 https://hdhive.com
	AllowPoints  bool   `json:"allow_points"`   // 是否允许消耗积分解锁（默认只解锁免费资源）
	Organize     bool   `json:"organize"`       // 转存后自动整理入库（默认开）
	TargetDir    string `json:"target_dir"`     // 解锁转存的目标目录（115 CID，空则回落分享同步的接收文件夹）
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

// hdhiveAPIKey 实际生效的 X-API-Key：个人 API Key / 自备应用 Secret 优先，回落内置应用。
// 个人 Key 单独填写即可用（无需 Client ID），所以不走 resolveCreds 的成对校验。
func (cfg *hdhiveCfg) hdhiveAPIKey() string {
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	_, secret, _, _ := cfg.resolveCreds()
	return secret
}

// hdhiveCall 调用影巢 OpenAPI（X-API-Key 鉴权，withToken 时附带用户 Bearer 令牌）。
// 响应壳：{success, code, message, data}
func hdhiveCall(cfg *hdhiveCfg, method, path string, body any, out any, withToken bool) error {
	secret := cfg.hdhiveAPIKey()
	if secret == "" {
		return fmt.Errorf("未配置影巢 API Key（影视转存 → 影巢 → 接入配置，个人 Key 或应用 Secret 均可）")
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
	req.Header.Set("X-API-Key", secret)
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
			msg = truncateStr(string(raw), 120)
		}
		return fmt.Errorf("影巢接口错误(HTTP %d): %s", resp.StatusCode, msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("影巢数据解析失败: %v", err)
		}
	}
	return nil
}

// hdhiveCallUser 用户级调用（搜资源/解锁/账号信息等业务接口）：
//   - 带 Bearer 令牌调用；401/403 且可续期 → refresh 后重试一次
//   - 续期失败或无令牌时降级为不带令牌再试一次（个人 API Key 自带用户身份的形态）
func hdhiveCallUser(cfg *hdhiveCfg, method, path string, body any, out any) error {
	if cfg.AccessToken == "" {
		if err := hdhiveCall(cfg, method, path, body, out, false); err == nil {
			return nil
		} else if isHdhiveAuthErr(err) {
			return fmt.Errorf("%s——请检查 API Key 是否有效，或在「影视转存 → 影巢」完成账号授权", err.Error())
		} else {
			return err
		}
	}
	err := hdhiveCall(cfg, method, path, body, out, true)
	if err == nil || !isHdhiveAuthErr(err) {
		return err
	}
	if cfg.RefreshToken != "" {
		if rerr := hdhiveRefreshToken(cfg); rerr == nil {
			log.Printf("[影巢] ↻ 令牌已自动续期，重试 %s", path)
			return hdhiveCall(cfg, method, path, body, out, true)
		} else {
			log.Printf("[影巢] ○ 令牌续期失败: %v", rerr)
		}
	}
	// 令牌失效且无法续期 → 降级不带令牌（个人 Key 部署形态仍可用）
	var fallback json.RawMessage
	if err2 := hdhiveCall(cfg, method, path, body, &fallback, false); err2 == nil {
		if out != nil && len(fallback) > 0 {
			return json.Unmarshal(fallback, out)
		}
		return nil
	}
	return err
}

// isHdhiveAuthErr 判断是否鉴权类错误（401/403）
func isHdhiveAuthErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "(HTTP 401)") || strings.Contains(s, "(HTTP 403)") ||
		strings.Contains(s, "未授权")
}

// ==================== OAuth 授权码流程 ====================

// hdhiveOAuthEntry 一次授权会话：换令牌必须用与发起授权相同的应用凭据和
// redirect_uri，存在 state 里随会话走（中途改配置不影响进行中的授权）
type hdhiveOAuthEntry struct {
	Exp        int64  `json:"exp"`        // 过期时间（unix 秒）
	Redirect   string `json:"redirect"`   // 发起授权时用的 redirect_uri（本地回调或中转页）
	Callback   string `json:"callback"`   // 本部署的回调地址（中转页转发目标）
	ClientID   string `json:"client_id"`  // 发起时用的应用
	Secret     string `json:"secret"`
}

// hdhiveOAuthStates 一次性 state（CSRF 防护），10 分钟有效。
// key 是随机 nonce；中转模式下 state 会带上中转页需要的转发目标
// （nonce + "." + base64url(callback)），nonce 本身不含点号不冲突
var hdhiveOAuthStates = struct {
	sync.Mutex
	m map[string]hdhiveOAuthEntry
}{m: map[string]hdhiveOAuthEntry{}}

func hdhiveStatePut(e hdhiveOAuthEntry) string {
	b := make([]byte, 16)
	rand.Read(b)
	nonce := hex.EncodeToString(b)
	e.Exp = time.Now().Add(10 * time.Minute).Unix()
	hdhiveOAuthStates.Lock()
	// 顺手清理过期项
	for k, old := range hdhiveOAuthStates.m {
		if time.Now().Unix() > old.Exp {
			delete(hdhiveOAuthStates.m, k)
		}
	}
	hdhiveOAuthStates.m[nonce] = e
	hdhiveOAuthStates.Unlock()
	if e.Callback != "" && e.Callback != e.Redirect {
		// 中转模式：state 附带转发目标，中转页原样带回
		return nonce + "." + base64.RawURLEncoding.EncodeToString([]byte(e.Callback))
	}
	return nonce
}

func hdhiveStateTake(full string) (hdhiveOAuthEntry, bool) {
	nonce := full
	if i := strings.IndexByte(full, '.'); i > 0 {
		nonce = full[:i] // 中转模式：取点号前的 nonce
	}
	if nonce == "" {
		return hdhiveOAuthEntry{}, false
	}
	hdhiveOAuthStates.Lock()
	defer hdhiveOAuthStates.Unlock()
	e, ok := hdhiveOAuthStates.m[nonce]
	if !ok || time.Now().Unix() > e.Exp {
		delete(hdhiveOAuthStates.m, nonce)
		return hdhiveOAuthEntry{}, false
	}
	delete(hdhiveOAuthStates.m, nonce) // 单次有效
	return e, true
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
// 凭据：用户自备优先；未配置时回落镜像内置应用（配合中转页实现开箱授权）
func (h *Handler) HdhiveOAuthStart(c *gin.Context) {
	cfg := loadHdhiveCfg()
	clientID, secret, source, relay := cfg.resolveCreds()
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置自有 Client ID / Secret，且当前镜像未内置官方应用"})
		return
	}
	callback := hdhiveRedirectURI(c)
	redirect := callback
	if source == "builtin" && relay != "" {
		redirect = relay // 内置应用走中转页：影巢白名单只需登记这一个固定地址
	}
	state := hdhiveStatePut(hdhiveOAuthEntry{
		Redirect: redirect, Callback: callback, ClientID: clientID, Secret: secret,
	})
	authURL := fmt.Sprintf("%s/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&state=%s",
		cfg.BaseURL, url.QueryEscape(clientID), url.QueryEscape(redirect), url.QueryEscape(state))
	if source == "builtin" {
		log.Printf("[影巢] ▶ 内置应用发起授权（经中转 %s）", truncateStr(redirect, 70))
	} else {
		log.Printf("[影巢] ▶ 自有应用发起授权（回调 %s）", truncateStr(callback, 70))
	}
	c.JSON(http.StatusOK, gin.H{"url": authURL, "redirect_uri": redirect, "creds": source})
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
	entry, ok := hdhiveStateTake(c.Query("state"))
	if !ok {
		back(false, "授权状态校验失败（state 已过期，请重新发起授权）")
		return
	}
	if code == "" {
		back(false, "缺少授权码")
		return
	}
	cfg := loadHdhiveCfg()
	if cfg.BaseURL == "" {
		cfg.BaseURL = hdhiveDefaultBase
	}

	// 换取 access_token（凭据与 redirect_uri 必须和发起授权时一致；
	// 响应壳可能是 {data:{...}} 嵌套或平铺，两种都兼容）
	tokBody, _ := json.Marshal(map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     entry.ClientID,
		"client_secret": entry.Secret,
		"code":          code,
		"redirect_uri":  entry.Redirect,
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
	clientID, secret, _, _ := cfg.resolveCreds()
	if clientID == "" {
		return fmt.Errorf("应用凭据缺失，请重新授权")
	}
	tokBody, _ := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"client_secret": secret,
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

// hdhiveFetchUser 拉账号基本信息（/api/open/me 为主，老版本退化 /api/open/user）
func hdhiveFetchUser(cfg *hdhiveCfg) (map[string]any, error) {
	var u map[string]any
	err := hdhiveCallUser(cfg, http.MethodGet, "/api/open/me", nil, &u)
	if err != nil {
		u = map[string]any{}
		if err2 := hdhiveCallUser(cfg, http.MethodGet, "/api/open/user", nil, &u); err2 == nil {
			return u, nil
		}
		return nil, err
	}
	return u, nil
}

// HdhiveUser GET /hdhive/user → 授权状态 + 账号基本信息（过期自动刷新令牌）
// 个人 API Key 自带用户身份：没有 OAuth 令牌也能拉到账号信息（authorized=false 但带 user）
func (h *Handler) HdhiveUser(c *gin.Context) {
	cfg := loadHdhiveCfg()
	if cfg.AccessToken == "" && cfg.hdhiveAPIKey() == "" {
		_, _, source, _ := cfg.resolveCreds()
		c.JSON(http.StatusOK, gin.H{"authorized": false, "configured": source != "", "creds": source})
		return
	}
	// 令牌将过期且可刷新 → 先续期
	if cfg.AccessToken != "" && cfg.TokenExp > 0 && time.Now().Add(60*time.Second).Unix() > cfg.TokenExp {
		if err := hdhiveRefreshToken(cfg); err != nil {
			log.Printf("[影巢] ○ 令牌续期失败: %v", err)
		}
	}
	user := map[string]any{}
	if cfg.UserJSON != "" {
		json.Unmarshal([]byte(cfg.UserJSON), &user)
	}
	// 缓存为空（授权时拉取失败或刚填了个人 Key）→ 现场重拉一次
	var fetchErr error
	if len(user) == 0 {
		u, err := hdhiveFetchUser(cfg)
		if err == nil {
			user = u
			b, _ := json.Marshal(u)
			cfg.UserJSON = string(b)
			_ = saveHdhiveCfg(cfg)
		} else {
			fetchErr = err
		}
	}
	resp := gin.H{
		"authorized":    cfg.AccessToken != "",
		"configured":    true,
		"authorized_at": cfg.AuthorizedAt,
		"user":          user,
	}
	if fetchErr != nil {
		resp["fetch_error"] = fetchErr.Error()
	}
	c.JSON(http.StatusOK, resp)
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
	_, _, source, relay := cfg.resolveCreds()
	c.JSON(http.StatusOK, gin.H{
		"client_id":     cfg.ClientID,
		"api_key":       cfg.APIKey,
		"base_url":      cfg.BaseURL,
		"allow_points":  cfg.AllowPoints,
		"organize":      cfg.Organize,
		"target_dir":    cfg.TargetDir,
		"configured":    cfg.hdhiveAPIKey() != "",
		"authorized":    cfg.AccessToken != "",
		"authorized_at": cfg.AuthorizedAt,
		"creds":         source, // custom=自备 / builtin=镜像内置 / ""=未就绪
		"has_relay":     relay != "",
	})
}

// HdhiveSaveConfig POST /hdhive/config {client_id, api_key, target_dir, allow_points}
// api_key 既接受应用 Secret 也接受个人 API Key（hdhive.com 个人设置 → API 管理）
func (h *Handler) HdhiveSaveConfig(c *gin.Context) {
	var req struct {
		ClientID    string `json:"client_id"`
		APIKey      string `json:"api_key"`
		TargetDir   string `json:"target_dir"`
		AllowPoints *bool  `json:"allow_points"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadHdhiveCfg()
	cfg.ClientID = strings.TrimSpace(req.ClientID)
	cfg.APIKey = strings.TrimSpace(req.APIKey)
	cfg.TargetDir = strings.TrimSpace(req.TargetDir)
	if req.AllowPoints != nil {
		cfg.AllowPoints = *req.AllowPoints
	}
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
	if cfg.hdhiveAPIKey() == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置影巢 API Key（个人 Key 或应用 Secret 均可）"})
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

// ==================== 资源搜索与解锁转存 ====================
//
// GET  /api/open/resources/{movie|tv}/{tmdb_id}   资源列表（业务接口）
// POST /api/open/resources/unlock {"slug": ...}   解锁 → 网盘分享链接
//
// 列表默认排序对齐 MoviePilot agentresourceofficer 的 resource_sort_key：
// 115 优先 → 免积分优先 → 有效优先 → 4K > 1080P → 蓝光原盘/REMUX > WEB-DL → 标题

// hdhiveMStr 从资源字段取字符串（字段名多版本兼容，取第一个非空）
func hdhiveMStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case float64:
			if v != 0 {
				return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
			}
		}
	}
	return ""
}

func hdhiveMInt(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case string:
			var n int64
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

func hdhiveMSlice(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hdhiveResourceLess 资源列表默认排序（推荐序）
func hdhiveResourceLess(a, b map[string]any) bool {
	panRank := func(m map[string]any) int {
		switch strings.ToLower(hdhiveMStr(m, "pan_type", "drive_type", "netdisk", "cloud_type")) {
		case "115":
			return 0
		case "quark", "夸克":
			return 1
		}
		return 2
	}
	points := func(m map[string]any) int64 { return hdhiveMInt(m, "unlock_points", "points", "cost") }
	validRank := func(m map[string]any) int {
		v := strings.ToLower(hdhiveMStr(m, "validate_status", "status"))
		if v == "" || v == "valid" {
			return 0
		}
		return 1
	}
	resRank := func(m map[string]any) int {
		has := func(sub string) bool {
			for _, r := range hdhiveMSlice(m, "video_resolution") {
				if strings.Contains(strings.ToUpper(r), sub) {
					return true
				}
			}
			return false
		}
		if has("4K") || has("2160") {
			return 0
		}
		if has("1080") {
			return 1
		}
		return 2
	}
	srcRank := func(m map[string]any) int {
		src := hdhiveMSlice(m, "source")
		for _, s := range src {
			if strings.Contains(s, "蓝光") || strings.Contains(strings.ToUpper(s), "REMUX") {
				return 0
			}
		}
		for _, s := range src {
			u := strings.ToUpper(s)
			if strings.Contains(u, "WEB-DL") || strings.Contains(u, "WEBRIP") {
				return 1
			}
		}
		return 2
	}
	if c := panRank(a) - panRank(b); c != 0 {
		return c < 0
	}
	pa, pb := points(a), points(b)
	if (pa > 0) != (pb > 0) {
		return pa == 0 // 免积分优先
	}
	if c := validRank(a) - validRank(b); c != 0 {
		return c < 0
	}
	if c := resRank(a) - resRank(b); c != 0 {
		return c < 0
	}
	if c := srcRank(a) - srcRank(b); c != 0 {
		return c < 0
	}
	return hdhiveMStr(a, "title", "name") < hdhiveMStr(b, "title", "name")
}

// HdhiveResources GET /hdhive/resources?media_type=movie|tv&tmdb_id=123
// → {data: [...]}（已按推荐序排序）
func (h *Handler) HdhiveResources(c *gin.Context) {
	cfg := loadHdhiveCfg()
	mediaType := strings.ToLower(strings.TrimSpace(c.Query("media_type")))
	if mediaType != "movie" && mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type 必须是 movie 或 tv"})
		return
	}
	tmdbID := strings.TrimSpace(c.Query("tmdb_id"))
	if tmdbID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 tmdb_id"})
		return
	}
	path := fmt.Sprintf("/api/open/resources/%s/%s", mediaType, url.PathEscape(tmdbID))
	q := url.Values{}
	if v := strings.TrimSpace(c.Query("page")); v != "" {
		q.Set("page", v)
	}
	if v := strings.TrimSpace(c.Query("page_size")); v != "" {
		q.Set("page_size", v)
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	raw, err := hdhiveCallRaw(cfg, http.MethodGet, path, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	items := hdhiveParseResourceList(raw)
	if items == nil {
		c.JSON(http.StatusOK, gin.H{"data": []any{}})
		return
	}
	sort.Slice(items, func(i, j int) bool { return hdhiveResourceLess(items[i], items[j]) })
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// hdhiveCallRaw 用户级调用并返回 data 原始 JSON（401/403 自动续期重试一次）
func hdhiveCallRaw(cfg *hdhiveCfg, method, path string, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := hdhiveCallUser(cfg, method, path, body, &out)
	return out, err
}

// hdhiveParseResourceList 兼容 data 为数组或 {resources|list|items: [...]} 两种结构
func hdhiveParseResourceList(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil && arr != nil {
		return arr
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	for _, k := range []string{"resources", "list", "items"} {
		children, ok := obj[k].([]any)
		if !ok {
			continue
		}
		var out []map[string]any
		for _, it := range children {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		if out != nil {
			return out
		}
	}
	return nil
}

// hdhiveExtractShareLink 从 unlock 响应里提取网盘链接与访问码（字段名多版本兼容）
func hdhiveExtractShareLink(data map[string]any) (link, pass string) {
	if data == nil {
		return "", ""
	}
	link = hdhiveMStr(data, "full_link", "share_link", "share_url", "pan_url", "url", "link")
	pass = hdhiveMStr(data, "password", "access_code", "receive_code", "pwd")
	if pass == "" {
		if m := reSharePass.FindStringSubmatch(link); m != nil {
			pass = m[1]
		}
	}
	return link, pass
}

// HdhiveUnlock POST /hdhive/unlock {slug, points}
// 解锁资源 → 提取网盘链接：115 分享链接自动转存到目标目录（转存后自动整理），
// 磁力/ed2k/HTTP 提交 115 离线下载，其他网盘返回链接由用户手动处理
func (h *Handler) HdhiveUnlock(c *gin.Context) {
	var req struct {
		Slug   string `json:"slug"`
		Points int64  `json:"points"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 slug"})
		return
	}
	cfg := loadHdhiveCfg()
	if req.Points > 0 && !cfg.AllowPoints {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("该资源需消耗 %d 积分解锁；如需自动消耗，请在影巢配置里开启「允许消耗积分」", req.Points)})
		return
	}
	raw, err := hdhiveCallRaw(cfg, http.MethodPost, "/api/open/resources/unlock", map[string]any{"slug": req.Slug})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	link, pass := hdhiveExtractShareLink(data)
	if link == "" {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "解锁成功但未解析到网盘链接（响应结构待适配）",
			"data":  raw,
		})
		return
	}
	log.Printf("[影巢] ✓ 解锁成功 slug=%s link=%s", truncateStr(req.Slug, 40), truncateStr(link, 70))

	switch {
	case is115ShareLink(link):
		msg, success, fail, terr := h.shareReceiveCore(link, pass, cfg.TargetDir, cfg.Organize)
		if terr != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "解锁成功，但 115 转存失败: " + terr.Error(),
				"link":  link,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "已转存（" + msg + "）",
			"link":    link,
			"count":   success,
			"failed":  fail,
			"note":    "转存成功，增量同步已自动触发（约 30 秒后完成 STRM 生成）",
		})
	case strings.HasPrefix(link, "magnet:") || strings.HasPrefix(link, "ed2k:") || strings.HasPrefix(link, "http"):
		if err := h.submitOfflineLink(link); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "解锁成功，但离线提交失败: " + err.Error(),
				"link":  link,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "解锁成功，已提交 115 离线下载（完成后自动整理入库）",
			"link":    link,
		})
	default:
		// 夸克/UC 等其他网盘：暂无转存通道，链接交用户手动处理
		c.JSON(http.StatusOK, gin.H{
			"message": "解锁成功。该网盘暂不支持自动转存，链接已生成，可复制后自行保存",
			"link":    link,
			"manual":  true,
		})
	}
}
