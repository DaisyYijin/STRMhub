package api

// ==================== HDHive（影巢）网页通道接入 ====================
//
// 影巢的官方凭证（API Key / OAuth 应用）全部需要站方审核，因此本通道走
// 网页账号密码（零审核），对齐 MoviePilot agentresourceofficer / p115strmhelper
// 的网页实现：
//
//   - 登录：POST /api/customer/user/login {username,password}（备选
//     /api/customer/auth/login），取 Set-Cookie（token/refresh_token/...）
//     或响应体 meta.access_token；GET /api/customer/user/info 校验登录态
//   - 会话：Cookie（含 cf_clearance）+ 绑定 UA 持久化，失效自动重登
//   - Cloudflare：影巢对数据中心 IP/非浏览器指纹直接 403 封禁页。直连
//     （Chrome 指纹 tls-client）失败时走 FlareSolverr 过盾：渲染页面 +
//     取 cf_clearance Cookie，之后同 IP 直连请求即可通过
//   - 搜索：GET /tmdb/{movie|tv}/{tmdb_id} 渲染页 → 解析资源卡片
//     （标题/大小/分辨率/积分/发布时间，规则对齐 _SCRAPE_CARDS_JS）
//   - 解锁：GET /resource/115/{slug} 渲染页 → 正则提取 115 分享链接；
//     免费/已解锁资源直接出链接，付费资源需先在影巢网页解锁（点「确定
//     解锁」的动作只有真实浏览器能触发），解锁后回到本页重试即可转存
//   - 转存：115 分享链接走 shareReceiveCore 四步转存 → 自动整理入库

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
)

const hdhiveDefaultBase = "https://hdhive.com"

// 影巢是 Chrome 用户的站点，UA 必须和过盾浏览器一致（cf_clearance 与 UA 绑定）
const hdhiveDefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	reHdhiveDate    = regexp.MustCompile(`发布于\s*([0-9/\-]+)`)
	reHdhiveSize    = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(TB|GB|MB|T|G|M)\b`)
	reHdhiveRes     = regexp.MustCompile(`(?i)\b(4K|8K|2K|1080[IP]|720[IP]|480[IP])\b`)
	reHdhivePoints  = regexp.MustCompile(`(\d+)\s*积分`)
	reHdhive115Link = regexp.MustCompile(`https?://(?:115cdn|115)\.com/[^\s"'<>）)]+`)
	reHdhiveSlug    = regexp.MustCompile(`^/resource/115/([A-Za-z0-9_-]+)`)
)

// hdhiveCfg 影巢配置（setting key "hdhive"）
type hdhiveCfg struct {
	BaseURL     string            `json:"base_url"`     // 默认 https://hdhive.com
	Username    string            `json:"username"`     // 影巢账号
	Password    string            `json:"password"`     // 影巢密码
	FlareURL    string            `json:"flare_url"`    // FlareSolverr 地址（http://ip:8191），可选
	TargetDir   string            `json:"target_dir"`   // 解锁转存目标目录（空则回落分享同步接收文件夹）
	AllowPoints bool              `json:"allow_points"` // 预留：付费资源提示开关（网页通道付费解锁需站内操作）
	Organize    bool              `json:"organize"`     // 转存后自动整理入库（默认开）
	Cookies     map[string]string `json:"cookies"`      // 会话 Cookie（token + cf_clearance 等）
	UA          string            `json:"ua"`           // 过盾时使用的 UA（Cookie 与其绑定）
	LoginAt     string            `json:"login_at"`     // 登录时间（展示用）
	UserJSON    string            `json:"user_json"`    // 登录后拉到的账号信息（缓存）
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
	if cfg.UA == "" {
		cfg.UA = hdhiveDefaultUA
	}
	if cfg.Cookies == nil {
		cfg.Cookies = map[string]string{}
	}
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

// hdhiveSave 在修改 cfg 后统一落盘（含缓存失效）
func hdhiveSave(cfg *hdhiveCfg) {
	if err := saveHdhiveCfg(cfg); err != nil {
		log.Printf("[影巢] ○ 配置保存失败: %v", err)
	}
}

// ==================== HTTP 会话层（Chrome 指纹直连 + FlareSolverr 过盾） ====================

var hdhiveHTTPOnce sync.Once
var hdhiveHTTPClient tlsclient.HttpClient
var hdhiveHTTPErr error

// hdhiveHTTP Chrome 131 指纹客户端（影巢 WAF 对 Go 默认 TLS 指纹直接封禁页）。
// 不用自动 CookieJar：Cookie 由 cfg.Cookies 统一管理（cf_clearance 与登录态一起持久化）。
func hdhiveHTTP() (tlsclient.HttpClient, error) {
	hdhiveHTTPOnce.Do(func() {
		hdhiveHTTPClient, hdhiveHTTPErr = tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
			tlsclient.WithTimeoutSeconds(25),
			tlsclient.WithClientProfile(profiles.Chrome_131),
			tlsclient.WithNotFollowRedirects(),
		)
	})
	return hdhiveHTTPClient, hdhiveHTTPErr
}

// hdhiveCookieHeader 把配置里的 Cookie 拼成请求头
func hdhiveCookieHeader(cfg *hdhiveCfg) string {
	parts := make([]string, 0, len(cfg.Cookies))
	for k, v := range cfg.Cookies {
		if k != "" && v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

// hdhiveIsCFBlock 判断响应是否为 Cloudflare 拦截页（封禁页/挑战页）
func hdhiveIsCFBlock(status int, body string) bool {
	if status != 403 && status != 503 && status != 429 {
		return false
	}
	return strings.Contains(body, "Attention Required") ||
		strings.Contains(body, "Just a moment") ||
		strings.Contains(body, "cf-challenge") ||
		strings.Contains(body, "cloudflare")
}

// hdhiveDirect 直连请求（带会话 Cookie + UA），返回响应体；不跟随重定向
func hdhiveDirect(cfg *hdhiveCfg, method, rawURL, body string, hdr map[string]string) (int, string, *fhttp.Response, error) {
	client, err := hdhiveHTTP()
	if err != nil {
		return 0, "", nil, err
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := fhttp.NewRequest(method, rawURL, rd)
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("User-Agent", cfg.UA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if ck := hdhiveCookieHeader(cfg); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, string(raw), resp, nil
}

// hdhiveImportRespCookies 把 Set-Cookie 合并进配置（登录/过盾后调用）
func hdhiveImportRespCookies(cfg *hdhiveCfg, resp *fhttp.Response) {
	if resp == nil {
		return
	}
	for _, ck := range resp.Cookies() {
		if ck.Name != "" && ck.Value != "" {
			cfg.Cookies[ck.Name] = ck.Value
		}
	}
}

// hdhiveFlareGet 经 FlareSolverr 渲染页面：返回 HTML、最终 URL。
// 顺带把过盾得到的 Cookie（cf_clearance 等）和 UA 存进配置——之后同 IP
// 直连请求带上它们即可通过 CF。
func (h *Handler) hdhiveFlareGet(cfg *hdhiveCfg, pageURL string) (string, string, error) {
	endpoint := strings.TrimRight(cfg.FlareURL, "/")
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	payload, _ := json.Marshal(map[string]any{
		"cmd":        "request.get",
		"url":        pageURL,
		"maxTimeout": 60000,
	})
	req, err := fhttp.NewRequest("POST", endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&fhttp.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("连接 FlareSolverr 失败: %s", sanitizeWecomErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Solution struct {
			Status    int    `json:"status"`
			URL       string `json:"url"`
			Response  string `json:"response"`
			UserAgent string `json:"userAgent"`
			Cookies   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		} `json:"solution"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", "", fmt.Errorf("FlareSolverr 响应解析失败: %s", truncateStr(string(raw), 150))
	}
	if out.Status != "ok" {
		return "", "", fmt.Errorf("FlareSolverr 过盾失败: %s", out.Message)
	}
	for _, ck := range out.Solution.Cookies {
		if ck.Name != "" && ck.Value != "" {
			cfg.Cookies[ck.Name] = ck.Value
		}
	}
	if out.Solution.UserAgent != "" {
		cfg.UA = out.Solution.UserAgent
	}
	hdhiveSave(cfg)
	log.Printf("[影巢] ✓ FlareSolverr 过盾成功（%d Cookie，HTTP %d）", len(out.Solution.Cookies), out.Solution.Status)
	return out.Solution.Response, out.Solution.URL, nil
}

// hdhiveEnsureSession 保证有能通过 CF 的会话：直连探测首页，被拦且配置了
// FlareSolverr 时自动过盾；都没配则给出明确指引
func (h *Handler) hdhiveEnsureSession(cfg *hdhiveCfg) error {
	if len(cfg.Cookies) > 0 {
		status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/api/customer/user/info", "", nil)
		if err == nil && !hdhiveIsCFBlock(status, body) {
			return nil
		}
	}
	if cfg.FlareURL != "" {
		_, _, err := h.hdhiveFlareGet(cfg, cfg.BaseURL+"/")
		return err
	}
	// 无 Cookie 也直连通过：站点未对本 IP 开 CF（部署环境不同策略不同）
	status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/", "", nil)
	if err == nil && !hdhiveIsCFBlock(status, body) {
		return nil
	}
	return fmt.Errorf("影巢站点被 Cloudflare 拦截（HTTP %d）。本服务 IP 无法直连时，请部署 FlareSolverr（docker run -d -p 8191:8191 flaresolverr/flaresolverr）并把地址填到影巢配置里", status)
}

// hdhivePage 拉取渲染后的页面 HTML：优先 FlareSolverr，未配置时直连（并诊断失败原因）
func (h *Handler) hdhivePage(cfg *hdhiveCfg, path string) (string, string, error) {
	abs := cfg.BaseURL + path
	if cfg.FlareURL != "" {
		htmlStr, finalURL, err := h.hdhiveFlareGet(cfg, abs)
		if err != nil {
			return "", "", err
		}
		return htmlStr, finalURL, nil
	}
	status, body, resp, err := hdhiveDirect(cfg, "GET", abs, "", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	})
	if err != nil {
		return "", "", fmt.Errorf("连接影巢失败: %s", sanitizeWecomErr(err))
	}
	if hdhiveIsCFBlock(status, body) {
		return "", "", fmt.Errorf("影巢站点被 Cloudflare 拦截（HTTP %d）。请部署 FlareSolverr（docker run -d -p 8191:8191 flaresolverr/flaresolverr）并把地址填到影巢配置里", status)
	}
	if status == 429 {
		return "", "", fmt.Errorf("影巢请求过快（HTTP 429），稍后再试")
	}
	final := abs
	if resp != nil && resp.Header.Get("Location") != "" {
		final = resp.Header.Get("Location")
	}
	return body, final, nil
}

// hdhiveIsLoginPage 判断最终落点是不是登录页（Cookie 失效的标志）
func hdhiveIsLoginPage(finalURL, body string) bool {
	if strings.Contains(finalURL, "/login") {
		return true
	}
	return strings.Contains(body, "login") && strings.Contains(body, "password") && len(body) < 20000
}

// ==================== 登录 ====================

var hdhiveLoginAPIs = []string{"/api/customer/user/login", "/api/customer/auth/login"}

// 登录页 Next.js Server Action 的路由状态树（对齐 MoviePilot agentresourceofficer）
const hdhiveLoginRouterState = `%5B%22%22%2C%7B%22children%22%3A%5B%22(auth)%22%2C%7B%22children%22%3A%5B%22login%22%2C%7B%22children%22%3A%5B%22__PAGE__%22%2C%7B%7D%2C%22%2Flogin%22%2C%22refresh%22%5D%7D%5D%7D%2Cnull%2Cnull%2Ctrue%5D%7D%2Cnull%2Cnull%2Ctrue%5D`

// 站内已知的历史登录 Action ID（页面解析失败时的最后尝试，可能随版本失效）
const hdhiveLoginActionFallback = "602b5a3af7ab2e93be6a14001ca83c1be491ccecea"

var (
	reHdhiveActionMeta   = regexp.MustCompile(`next-action"\s*:\s*"([a-fA-F0-9]{16,64})"`)
	reHdhiveActionForm   = regexp.MustCompile(`name="next-action"\s+value="([a-fA-F0-9]{16,64})"`)
	reHdhiveActionCreate = regexp.MustCompile(`createServerReference\("([a-f0-9]{40,})"[\s\S]{0,400}?"login"`)
	reHdhiveLoginChunk   = regexp.MustCompile(`<script[^>]+src="([^"]+/app/\(auth\)/login/page-[^"]+\.js)"`)
)

// hdhiveServerActionLogin Next.js Server Action 方式登录：
// 打开登录页挖出 login action ID → POST 登录页带 next-action 头 → 收 Set-Cookie
func (h *Handler) hdhiveServerActionLogin(cfg *hdhiveCfg) (bool, string) {
	// 1. 预热登录页（拿 HTML 里的 action ID 与 CF Cookie）
	status, warm, resp, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/login", "", map[string]string{
		"Accept":  "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Referer": cfg.BaseURL + "/",
	})
	if err != nil {
		return false, "打开影巢登录页失败: " + sanitizeWecomErr(err)
	}
	if hdhiveIsCFBlock(status, warm) {
		return false, fmt.Sprintf("登录页被 Cloudflare 拦截（HTTP %d）", status)
	}
	hdhiveImportRespCookies(cfg, resp)

	// 2. 挖 login action ID：页面内联 → 登录页 JS chunk → 内置兜底
	actionID := ""
	for _, re := range []*regexp.Regexp{reHdhiveActionMeta, reHdhiveActionForm} {
		if m := re.FindStringSubmatch(warm); m != nil {
			actionID = m[1]
			break
		}
	}
	if actionID == "" {
		for _, m := range reHdhiveLoginChunk.FindAllStringSubmatch(warm, 5) {
			src := m[1]
			if !strings.HasPrefix(src, "http") {
				src = cfg.BaseURL + src
			}
			_, chunk, _, err := hdhiveDirect(cfg, "GET", src, "", map[string]string{
				"Accept":  "*/*",
				"Referer": cfg.BaseURL + "/login",
			})
			if err != nil {
				continue
			}
			if mm := reHdhiveActionCreate.FindStringSubmatch(chunk); mm != nil {
				actionID = mm[1]
				break
			}
		}
	}
	if actionID == "" {
		actionID = hdhiveLoginActionFallback
		log.Printf("[影巢] ○ 未解析到登录 Action ID，使用内置兜底值")
	} else {
		log.Printf("[影巢] ▶ 登录 Action ID: %s", truncateStr(actionID, 16))
	}

	// 3. POST 登录页触发 Server Action（body: [{"username","password"},"/"]）
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode([]any{map[string]string{"username": cfg.Username, "password": cfg.Password}, "/"})
	var raw string
	status, raw, resp, err = hdhiveDirect(cfg, "POST", cfg.BaseURL+"/login",
		strings.TrimRight(buf.String(), "\n"), map[string]string{
			"Accept":                 "text/x-component",
			"Content-Type":           "text/plain;charset=UTF-8",
			"Next-Action":            actionID,
			"Next-Router-State-Tree": hdhiveLoginRouterState,
			"Origin":                 cfg.BaseURL,
			"Referer":                cfg.BaseURL + "/login",
		})
	if err != nil {
		return false, "Server Action 登录请求失败: " + sanitizeWecomErr(err)
	}
	hdhiveImportRespCookies(cfg, resp)

	// 4. 判定结果：Set-Cookie 带 token 即成功；否则解析流式响应里的报错
	for _, ck := range resp.Cookies() {
		if ck.Name == "token" && ck.Value != "" {
			return true, ""
		}
	}
	if loc := resp.Header.Get("X-Action-Redirect"); strings.Contains(loc, "/login") {
		return false, "影巢拒绝登录（账号或密码错误）"
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "1:") {
			continue
		}
		var payload struct {
			Error struct {
				Message     string `json:"message"`
				Description string `json:"description"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line[2:]), &payload) == nil {
			msg := payload.Error.Message
			if msg == "" {
				msg = payload.Error.Description
			}
			if msg != "" {
				return false, "影巢登录失败: " + msg
			}
		}
	}
	return false, fmt.Sprintf("Server Action 登录未取到会话（HTTP %d）: %s", status, truncateStr(raw, 100))
}

// hdhiveCheckLogin 校验登录态（GET /api/customer/user/info）
func (h *Handler) hdhiveCheckLogin(cfg *hdhiveCfg) (bool, map[string]any) {
	if len(cfg.Cookies) == 0 {
		return false, nil
	}
	status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/api/customer/user/info", "", nil)
	if err != nil || status >= 400 || hdhiveIsCFBlock(status, body) {
		return false, nil
	}
	var out struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(body), &out) != nil || !out.Success || out.Data == nil {
		return false, nil
	}
	return true, out.Data
}

// HdhiveLogin POST /hdhive/login {username?, password?}（空则用已保存配置）
// 影巢网页登录：过 CF → POST 登录接口 → 收 Cookie/令牌 → 校验登录态
func (h *Handler) HdhiveLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&req)
	cfg := loadHdhiveCfg()
	if req.Username != "" {
		cfg.Username = strings.TrimSpace(req.Username)
	}
	if req.Password != "" {
		cfg.Password = req.Password
	}
	if cfg.Username == "" || cfg.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写影巢账号和密码"})
		return
	}
	if err := h.hdhiveEnsureSession(cfg); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// 登录请求：两个候选接口依次尝试
	type loginResult struct {
		token string // body 里带的 access_token（部分版本不放 Set-Cookie）
		msg   string
		ok    bool
	}
	var lr loginResult
	for _, apiPath := range hdhiveLoginAPIs {
		body, _ := json.Marshal(map[string]string{"username": cfg.Username, "password": cfg.Password})
		status, raw, resp, err := hdhiveDirect(cfg, "POST", cfg.BaseURL+apiPath, string(body), map[string]string{
			"Origin":  cfg.BaseURL,
			"Referer": cfg.BaseURL + "/login",
		})
		if err != nil {
			lr.msg = "连接影巢失败: " + sanitizeWecomErr(err)
			continue
		}
		if hdhiveIsCFBlock(status, raw) {
			lr.msg = fmt.Sprintf("登录接口被 Cloudflare 拦截（HTTP %d）", status)
			continue
		}
		hdhiveImportRespCookies(cfg, resp)
		var out struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Meta    struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"meta"`
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal([]byte(raw), &out)
		if out.Meta.AccessToken != "" {
			cfg.Cookies["token"] = out.Meta.AccessToken
			if out.Meta.RefreshToken != "" {
				cfg.Cookies["refresh_token"] = out.Meta.RefreshToken
			}
		}
		if out.Success || out.Meta.AccessToken != "" {
			lr.ok, lr.token = true, out.Meta.AccessToken
			break
		}
		lr.msg = out.Message
		if lr.msg == "" {
			lr.msg = fmt.Sprintf("HTTP %d: %s", status, truncateStr(raw, 120))
		}
	}
	if !lr.ok {
		// 旧版 JSON 登录接口已下线（404，站点改版为 Next.js）→ 走 Server Action 登录
		if ok2, msg2 := h.hdhiveServerActionLogin(cfg); ok2 {
			lr.ok = true
		} else if strings.Contains(lr.msg, "404") {
			lr.msg = msg2
		}
	}
	if !lr.ok {
		hdhiveSave(cfg)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录未通过: " + lr.msg})
		return
	}

	// 校验登录态并缓存账号信息
	ok, user := h.hdhiveCheckLogin(cfg)
	if !ok {
		hdhiveSave(cfg)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录后校验失败：Cookie 未生效（站点可能改版或账号受限），请重试"})
		return
	}
	cfg.LoginAt = time.Now().Format("2006-01-02 15:04")
	if user != nil {
		b, _ := json.Marshal(user)
		cfg.UserJSON = string(b)
	}
	hdhiveSave(cfg)
	log.Printf("[影巢] ✓ 账号 %s 登录成功", cfg.Username)
	c.JSON(http.StatusOK, gin.H{"message": "登录成功", "user": user, "login_at": cfg.LoginAt})
}

// HdhiveLogout POST /hdhive/logout → 清除会话
func (h *Handler) HdhiveLogout(c *gin.Context) {
	cfg := loadHdhiveCfg()
	cfg.Cookies = map[string]string{}
	cfg.UserJSON, cfg.LoginAt = "", ""
	hdhiveSave(cfg)
	log.Printf("[影巢] ○ 已退出登录")
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

// ==================== 配置 ====================

// HdhiveGetConfig GET /hdhive/config
func (h *Handler) HdhiveGetConfig(c *gin.Context) {
	cfg := loadHdhiveCfg()
	loggedIn, user := h.hdhiveCheckLogin(cfg)
	c.JSON(http.StatusOK, gin.H{
		"base_url":     cfg.BaseURL,
		"username":     cfg.Username,
		"password":     cfg.Password,
		"flare_url":    cfg.FlareURL,
		"target_dir":   cfg.TargetDir,
		"allow_points": cfg.AllowPoints,
		"organize":     cfg.Organize,
		"logged_in":    loggedIn,
		"login_at":     cfg.LoginAt,
		"user":         user,
		"has_cookie":   len(cfg.Cookies) > 0,
	})
}

// HdhiveSaveConfig POST /hdhive/config {base_url, username, password, flare_url, target_dir, allow_points}
func (h *Handler) HdhiveSaveConfig(c *gin.Context) {
	var req struct {
		BaseURL     string `json:"base_url"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		FlareURL    string `json:"flare_url"`
		TargetDir   string `json:"target_dir"`
		AllowPoints *bool  `json:"allow_points"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadHdhiveCfg()
	base := strings.TrimSpace(req.BaseURL)
	if base != "" {
		if !strings.Contains(base, "://") {
			base = "https://" + base
		}
		cfg.BaseURL = strings.TrimRight(base, "/")
	}
	if req.Username != "" {
		cfg.Username = strings.TrimSpace(req.Username)
	}
	if req.Password != "" {
		cfg.Password = req.Password
	}
	cfg.FlareURL = strings.TrimSpace(req.FlareURL)
	cfg.TargetDir = strings.TrimSpace(req.TargetDir)
	if req.AllowPoints != nil {
		cfg.AllowPoints = *req.AllowPoints
	}
	// 站点地址变更后旧会话作废
	if cfg.BaseURL != hdhiveDefaultBase && len(cfg.Cookies) > 0 {
		cfg.Cookies = map[string]string{}
	}
	hdhiveSave(cfg)
	log.Printf("[影巢] ✓ 接入配置已保存")
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// HdhiveTest POST /hdhive/test → 连通性 + 登录态（被 CF 拦时给出 FlareSolverr 指引）
func (h *Handler) HdhiveTest(c *gin.Context) {
	cfg := loadHdhiveCfg()
	status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/api/customer/user/info", "", nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "连接影巢失败: " + sanitizeWecomErr(err)})
		return
	}
	if hdhiveIsCFBlock(status, body) {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("影巢被 Cloudflare 拦截（HTTP %d）——请部署 FlareSolverr 并把地址填到影巢配置", status)})
		return
	}
	loggedIn, user := h.hdhiveCheckLogin(cfg)
	if !loggedIn {
		c.JSON(http.StatusOK, gin.H{"message": "站点连通，但未登录（请填写账号密码后点登录）", "logged_in": false})
		return
	}
	nick := ""
	if user != nil {
		if v, ok := user["nickname"].(string); ok {
			nick = v
		} else if v, ok := user["username"].(string); ok {
			nick = v
		} else if v, ok := user["email"].(string); ok {
			nick = v
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "连接成功，已登录" + hdhiveIf(nick != "", "："+nick, ""), "logged_in": true, "user": user})
}

func hdhiveIf(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// ==================== 资源搜索 ====================

// hdhiveCard 渲染页解析出的资源卡片（字段对齐 _SCRAPE_CARDS_JS）
type hdhiveCard struct {
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	Size         string   `json:"size"`
	Resolution   string   `json:"resolution"`
	PostedAt     string   `json:"posted_at"`
	User         string   `json:"user"`
	IsFree       bool     `json:"is_free"`
	UnlockPoints int64    `json:"unlock_points"`
	Tags         []string `json:"tags"`
}

// hdhiveCollectText 收集节点子树文本（等价 innerText 的近似：块级换行）
func hdhiveCollectText(n *html.Node, sb *strings.Builder) {
	if n.Type == html.TextNode {
		sb.WriteString(n.Data)
		return
	}
	if n.Type != html.ElementNode {
		return
	}
	tag := n.Data
	if tag == "script" || tag == "style" {
		return
	}
	block := tag == "div" || tag == "p" || tag == "li" || tag == "section" || tag == "article" || tag == "br" || tag == "tr"
	if block && sb.Len() > 0 {
		sb.WriteByte('\n')
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		hdhiveCollectText(ch, sb)
	}
	if block {
		sb.WriteByte('\n')
	}
}

// hdhiveParseCards 从渲染后的详情页 HTML 解析资源卡片：
// 遍历 /resource/115/ 链接，按文本特征提取标题/大小/分辨率/积分（对齐
// p115strmhelper _SCRAPE_CARDS_JS 的启发式）
func hdhiveParseCards(pageHTML string) []hdhiveCard {
	root, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var cards []hdhiveCard
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := ""
			for _, a := range n.Attr {
				if a.Key == "href" {
					href = a.Val
					break
				}
			}
			if m := reHdhiveSlug.FindStringSubmatch(href); m != nil && !seen[m[1]] {
				var sb strings.Builder
				hdhiveCollectText(n, &sb)
				if card, ok := hdhiveCardFromText(sb.String()); ok {
					card.Slug = m[1]
					seen[m[1]] = true
					cards = append(cards, card)
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	return cards
}

// hdhiveCardFromText 从卡片文本提取字段（标题行启发式过滤对齐原 JS）
func hdhiveCardFromText(text string) (hdhiveCard, bool) {
	if !strings.Contains(text, "发布于") || !reHdhiveSize.MatchString(text) {
		return hdhiveCard{}, false
	}
	if strings.Count(text, "发布于") != 1 {
		return hdhiveCard{}, false
	}
	card := hdhiveCard{IsFree: strings.Contains(text, "免费")}
	if m := reHdhiveDate.FindStringSubmatch(text); m != nil {
		card.PostedAt = m[1]
	}
	if m := reHdhiveSize.FindStringSubmatch(text); m != nil {
		card.Size = strings.ToUpper(m[1] + " " + m[2])
		if !strings.HasSuffix(card.Size, "B") {
			card.Size += "B"
		}
	}
	if m := reHdhiveRes.FindStringSubmatch(text); m != nil {
		card.Resolution = strings.ToUpper(m[1])
	}
	if m := reHdhivePoints.FindStringSubmatch(text); m != nil {
		var n int64
		fmt.Sscanf(m[1], "%d", &n)
		card.UnlockPoints = n
	}
	if card.IsFree {
		card.UnlockPoints = 0
	}
	if strings.Contains(text, "官组") || strings.Contains(text, "管理员") {
		card.Tags = append(card.Tags, "官组")
	}
	if card.IsFree {
		card.Tags = append(card.Tags, "免费")
	} else if card.UnlockPoints > 0 {
		card.Tags = append(card.Tags, fmt.Sprintf("%d 积分", card.UnlockPoints))
	}

	// 标题：过滤掉元数据行（发布于/积分/纯大小/发布者）后的剩余行
	metaTerms := map[string]bool{
		"4K": true, "8K": true, "2K": true, "1080P": true, "1080p": true, "720P": true, "720p": true, "480P": true, "480p": true,
		"免费": true, "官组": true, "管理员": true, "ISO": true,
		"WEB-DL": true, "WEBRip": true, "BDRip": true, "REMUX": true, "HDTV": true,
		"简中": true, "繁中": true, "简英": true, "繁英": true, "内封": true, "外挂": true, "内嵌": true,
		"简日": true, "繁日": true, "简韩": true, "繁韩": true, "蓝光原盘": true,
	}
	rePureSize := regexp.MustCompile(`(?i)^\d+\.?\d*\s*(T?B|G[Bi]?|M[Bi]?)$`)
	rePurePoints := regexp.MustCompile(`^\d+\s*积分$`)
	lines := strings.Split(text, "\n")
	dateLine := -1
	cleaned := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.Contains(l, "发布于") {
			cleaned = append(cleaned, l) // 先占位，最后去掉
			dateLine = len(cleaned) - 1
			continue
		}
		cleaned = append(cleaned, l)
	}
	user := ""
	if dateLine > 0 {
		user = cleaned[dateLine-1]
	}
	var titleLines []string
	for i, l := range cleaned {
		if l == "" || len(l) < 3 || metaTerms[l] {
			continue
		}
		if i == dateLine || strings.HasPrefix(l, "发布于") || rePurePoints.MatchString(l) || rePureSize.MatchString(l) {
			continue
		}
		if l == user {
			continue
		}
		titleLines = append(titleLines, strings.TrimPrefix(l, "免费"))
	}
	card.Title = strings.TrimSpace(strings.Join(titleLines, " "))
	if len(card.Title) > 220 {
		card.Title = card.Title[:220]
	}
	if card.Title == "" {
		card.Title = card.Size
	}
	return card, card.Title != "" || card.Size != ""
}

// hdhiveCardLess 推荐序：免费优先 → 4K/1080P 优先 → 大 → 时间新
func hdhiveCardLess(a, b hdhiveCard) bool {
	if a.IsFree != b.IsFree {
		return a.IsFree
	}
	resRank := func(r string) int {
		switch {
		case strings.Contains(r, "4K"), strings.Contains(r, "8K"):
			return 0
		case strings.Contains(r, "1080"):
			return 1
		}
		return 2
	}
	if c := resRank(a.Resolution) - resRank(b.Resolution); c != 0 {
		return c < 0
	}
	if c := hdhiveSizeBytes(a.Size) - hdhiveSizeBytes(b.Size); c != 0 {
		return c > 0
	}
	return a.PostedAt > b.PostedAt
}

// hdhiveSizeBytes "10.49 GB" → 字节数（排序用）
func hdhiveSizeBytes(s string) int64 {
	m := reHdhiveSize.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	var v float64
	fmt.Sscanf(m[1], "%f", &v)
	var mult float64 = 1 << 20
	switch strings.ToUpper(m[2]) {
	case "T", "TB":
		mult = 1 << 40
	case "G", "GB":
		mult = 1 << 30
	case "M", "MB":
		mult = 1 << 20
	}
	return int64(v * mult)
}

// HdhiveResources GET /hdhive/resources?media_type=movie|tv&tmdb_id=123
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
	pageHTML, finalURL, err := h.hdhivePage(cfg, "/tmdb/"+mediaType+"/"+url.PathEscape(tmdbID))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if hdhiveIsLoginPage(finalURL, pageHTML) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "影巢登录已失效，请重新登录"})
		return
	}
	cards := hdhiveParseCards(pageHTML)
	sort.Slice(cards, func(i, j int) bool { return hdhiveCardLess(cards[i], cards[j]) })
	c.JSON(http.StatusOK, gin.H{"data": cards})
}

// ==================== 解锁与转存 ====================

// hdhiveTrimLink 去掉链接尾部被 HTML/正文带入的标点
func hdhiveTrimLink(s string) string {
	return strings.TrimRight(s, ".,;。，；）】」\"'#")
}

// HdhiveUnlock POST /hdhive/unlock {slug}
// 打开资源页提取 115 分享链接 → 自动转存。免费/已解锁资源页面直接带链接；
// 付费资源需先在影巢网页点「确定解锁」（仅真实浏览器可触发），再回来重试。
func (h *Handler) HdhiveUnlock(c *gin.Context) {
	var req struct {
		Slug string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 slug"})
		return
	}
	cfg := loadHdhiveCfg()
	pageHTML, finalURL, err := h.hdhivePage(cfg, "/resource/115/"+url.PathEscape(req.Slug))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if hdhiveIsLoginPage(finalURL, pageHTML) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "影巢登录已失效，请重新登录"})
		return
	}
	link := ""
	if m := reHdhive115Link.FindString(pageHTML); m != "" {
		link = hdhiveTrimLink(m)
	}
	if link == "" {
		hint := "该资源需要解锁：请在影巢网页端打开此资源并点「确定解锁」（付费解锁只能在站内完成），完成后回到这里重试即可自动转存"
		if strings.Contains(pageHTML, "积分不足") {
			hint = "影巢积分不足，无法解锁该资源"
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": hint,
			"page":  cfg.BaseURL + "/resource/115/" + req.Slug,
		})
		return
	}
	log.Printf("[影巢] ✓ 提取到分享链接 slug=%s link=%s", truncateStr(req.Slug, 40), truncateStr(link, 70))
	pass := ""
	if m := reSharePass.FindStringSubmatch(link); m != nil {
		pass = m[1]
	}

	switch {
	case is115ShareLink(link):
		msg, success, fail, terr := h.shareReceiveCore(link, pass, cfg.TargetDir, cfg.Organize)
		if terr != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "提取到分享链接，但 115 转存失败: " + terr.Error(),
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
			"message": "已提交 115 离线下载（完成后自动整理入库）",
			"link":    link,
		})
	default:
		c.JSON(http.StatusOK, gin.H{
			"message": "提取到链接，但暂不支持自动转存该协议，请手动处理",
			"link":    link,
			"manual":  true,
		})
	}
}
