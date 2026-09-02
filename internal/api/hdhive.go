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
//   - Cloudflare：影巢对数据中心 IP/非浏览器指纹可能 403 封禁页，
//     直连（Chrome 指纹 tls-client）被拦时会给出明确报错
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

// ==================== HTTP 会话层（Chrome 指纹直连） ====================

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

// hdhiveEnsureSession 保证有能通过 CF 的会话：直连探测首页，被拦且配置了
// FlareSolverr 时自动过盾；都没配则给出明确指引
func (h *Handler) hdhiveEnsureSession(cfg *hdhiveCfg) error {
	if len(cfg.Cookies) > 0 {
		status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/api/customer/user/info", "", nil)
		if err == nil && !hdhiveIsCFBlock(status, body) {
			return nil
		}
	}
	// 无 Cookie 也直连通过：站点未对本 IP 开 CF（部署环境不同策略不同）
	status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/", "", nil)
	if err == nil && !hdhiveIsCFBlock(status, body) {
		return nil
	}
	return fmt.Errorf("影巢站点被 Cloudflare 拦截（HTTP %d）：当前服务器 IP 无法直连影巢", status)
}

// hdhivePage 拉取渲染后的页面 HTML：优先 FlareSolverr，未配置时直连（并诊断失败原因）
func (h *Handler) hdhivePage(cfg *hdhiveCfg, path string) (string, string, error) {
	abs := cfg.BaseURL + path
	status, body, resp, err := hdhiveDirect(cfg, "GET", abs, "", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	})
	if err != nil {
		return "", "", fmt.Errorf("连接影巢失败: %s", sanitizeWecomErr(err))
	}
	if hdhiveIsCFBlock(status, body) {
		return "", "", fmt.Errorf("影巢站点被 Cloudflare 拦截（HTTP %d）：当前服务器 IP 无法直连影巢", status)
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
// hdhiveLoginWall 通过内容识别登录墙：站点对未登录访问不重定向，
// 而是在原 URL 渲染登录页（标记串取自实测登录页），命中 ≥2 个即判定
func hdhiveLoginWall(body string) bool {
	markers := []string{"忘记密码", "马上注册", "登录您的账号", "用户名或邮箱"}
	hit := 0
	for _, m := range markers {
		if strings.Contains(body, m) {
			hit++
		}
	}
	return hit >= 2
}

func hdhiveIsLoginPage(finalURL, body string) bool {
	if strings.Contains(finalURL, "/login") {
		return true
	}
	return hdhiveLoginWall(body)
}

// ==================== 登录 ====================

var hdhiveLoginAPIs = []string{"/api/customer/user/login", "/api/customer/auth/login"}

// 登录页 Next.js Server Action 的路由状态树（对齐 MoviePilot agentresourceofficer）
const hdhiveLoginRouterState = `%5B%22%22%2C%7B%22children%22%3A%5B%22(auth)%22%2C%7B%22children%22%3A%5B%22login%22%2C%7B%22children%22%3A%5B%22__PAGE__%22%2C%7B%7D%2C%22%2Flogin%22%2C%22refresh%22%5D%7D%5D%7D%2Cnull%2Cnull%2Ctrue%5D%7D%2Cnull%2Cnull%2Ctrue%5D`

// 站内已知的历史登录 Action ID（页面解析失败时的最后尝试，可能随版本失效）
const hdhiveLoginActionFallback = "602b5a3af7ab2e93be6a14001ca83c1be491ccecea"

var (
	reHdhiveActionID     = regexp.MustCompile(`\$ACTION_ID_([a-f0-9]{40,})`)
	reHdhiveActionMeta   = regexp.MustCompile(`(?i)next-action"?\s*[:=]\s*"([a-fA-F0-9]{16,64})"`)
	reHdhiveActionForm   = regexp.MustCompile(`name="next-action"\s+value="([a-fA-F0-9]{16,64})"`)
	reHdhiveActionCreate = regexp.MustCompile(`createServerReference\("([a-f0-9]{40,})"[\s\S]{0,900}?"login"`)
	reHdhiveActionReg    = regexp.MustCompile(`(?i)registerServerReference\([^,]{0,80},\s*"([a-f0-9]{40,})"[\s\S]{0,300}?"login"`)
	reHdhiveScriptSrc    = regexp.MustCompile(`(?i)<script[^>]+src="([^"]+_next/static/[^"]+\.js)"`)
	reHdhiveLoginChunk   = regexp.MustCompile(`(?i)<script[^>]+src="([^"]*login[^"]*\.js)"`)
	reHdhiveHexStr       = regexp.MustCompile(`"([a-f0-9]{40,64})"`)
)

// hdhiveHexNearLogin 在 chunk 文本里找「login」附近（±600 字符）的 40+ 位十六进制
// action ID——对压缩器变体最宽容的挖法
func hdhiveHexNearLogin(chunk string) string {
	lower := strings.ToLower(chunk)
	for i := 0; ; {
		idx := strings.Index(lower[i:], "login")
		if idx < 0 {
			return ""
		}
		at := i + idx
		start, end := at-600, at+660
		if start < 0 {
			start = 0
		}
		if end > len(chunk) {
			end = len(chunk)
		}
		if m := reHdhiveHexStr.FindAllStringSubmatch(chunk[start:end], -1); len(m) > 0 {
			return m[0][1]
		}
		i = at + 5
	}
}

// hdhiveServerActionLogin Next.js Server Action 方式登录：
// 从登录页 HTML/JS chunk 多来源收集候选 action ID 并逐个尝试，
// 从 Set-Cookie 取会话；站点明确拒绝（账号密码错误）时立即返回原文报错
func (h *Handler) hdhiveServerActionLogin(cfg *hdhiveCfg) (bool, string) {
	// 1. 预热登录页（拿 HTML 与 CF Cookie）
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

	// 2. 收集候选 action ID（按可信度排序）
	type actionCandidate struct{ from, id string }
	var ids []actionCandidate
	seen := map[string]bool{}
	push := func(from, id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, actionCandidate{from, id})
		}
	}
	if m := reHdhiveActionID.FindStringSubmatch(warm); m != nil {
		push("page-form", m[1])
	}
	if m := reHdhiveActionMeta.FindStringSubmatch(warm); m != nil {
		push("page-meta", m[1])
	}
	if m := reHdhiveActionForm.FindStringSubmatch(warm); m != nil {
		push("page-form2", m[1])
	}

	// 候选 chunk：优先路径含 login 的，其次登录页引用的全部 _next 静态 chunk
	var chunks []string
	chunkSeen := map[string]bool{}
	addChunk := func(src string) {
		if !strings.HasPrefix(src, "http") {
			src = cfg.BaseURL + src
		}
		if !chunkSeen[src] {
			chunkSeen[src] = true
			chunks = append(chunks, src)
		}
	}
	for _, m := range reHdhiveLoginChunk.FindAllStringSubmatch(warm, 10) {
		addChunk(m[1])
	}
	loginChunkCount := len(chunks)
	for _, m := range reHdhiveScriptSrc.FindAllStringSubmatch(warm, 30) {
		addChunk(m[1])
	}
	fetched := 0
	for i, src := range chunks {
		if fetched >= 12 {
			break
		}
		fetched++
		_, chunk, _, err := hdhiveDirect(cfg, "GET", src, "", map[string]string{
			"Accept":  "*/*",
			"Referer": cfg.BaseURL + "/login",
		})
		if err != nil {
			continue
		}
		if m := reHdhiveActionCreate.FindStringSubmatch(chunk); m != nil {
			push(fmt.Sprintf("chunk%d-csr", i), m[1])
		}
		if m := reHdhiveActionReg.FindStringSubmatch(chunk); m != nil {
			push(fmt.Sprintf("chunk%d-reg", i), m[1])
		}
		if id := hdhiveHexNearLogin(chunk); id != "" {
			pref := fmt.Sprintf("chunk%d-near", i)
			if i < loginChunkCount {
				pref += "-loginjs"
			}
			push(pref, id)
		}
	}
	push("builtin", hdhiveLoginActionFallback)
	log.Printf("[影巢] ▶ Server Action 候选 %d 个（页面 %d 字节，chunk %d 个/含 login 命名 %d 个，抓取 %d）",
		len(ids), len(warm), len(chunks), loginChunkCount, fetched)

	// 3. 逐个尝试，直到拿到会话或站点明确拒绝
	var lastErr string
	for _, cand := range ids {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.Encode([]any{map[string]string{"username": cfg.Username, "password": cfg.Password}, "/"})
		status, raw, resp, err := hdhiveDirect(cfg, "POST", cfg.BaseURL+"/login",
			strings.TrimRight(buf.String(), "\n"), map[string]string{
				"Accept":                 "text/x-component",
				"Content-Type":           "text/plain;charset=UTF-8",
				"Next-Action":            cand.id,
				"Next-Router-State-Tree": hdhiveLoginRouterState,
				"Origin":                 cfg.BaseURL,
				"Referer":                cfg.BaseURL + "/login",
			})
		if err != nil {
			return false, "Server Action 登录请求失败: " + sanitizeWecomErr(err)
		}
		hdhiveImportRespCookies(cfg, resp)
		hasToken := false
		for _, ck := range resp.Cookies() {
			if ck.Name == "token" && ck.Value != "" {
				hasToken = true
			}
		}
		if hasToken {
			// 鉴权探测：登录墙消失才算真的登录成功（错误 action 也可能下发 token）
			pstatus, pbody, presp, perr := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/user", "", nil)
			walled := perr != nil || hdhiveIsCFBlock(pstatus, pbody)
			if !walled {
				loc := ""
				if presp != nil {
					loc = presp.Header.Get("Location")
				}
				walled = strings.Contains(loc, "/login") || hdhiveLoginWall(pbody)
			}
			if !walled {
				log.Printf("[影巢] ✓ 登录成功（action 来源 %s）", cand.from)
				return true, ""
			}
			log.Printf("[影巢] ○ action[%s] 下发了 token 但仍在登录墙，换下一个候选", cand.from)
			delete(cfg.Cookies, "token")
		}
		if loc := resp.Header.Get("X-Action-Redirect"); strings.Contains(loc, "/login") {
			return false, "影巢拒绝登录（账号或密码错误）"
		}
		// 站点业务报错（action 有效但登录被拒）→ 立即透出原文
		rejected := ""
		if strings.Contains(raw, "账号") || strings.Contains(raw, "密码") || strings.Contains(raw, "用户名") {
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
					rejected = payload.Error.Message
					if rejected == "" {
						rejected = payload.Error.Description
					}
				}
			}
			if rejected != "" {
				return false, "影巢登录失败: " + rejected
			}
			return false, "影巢登录失败（站点返回含账号/密码字样的响应，未解析出明细）"
		}
		lastErr = fmt.Sprintf("action[%s]=%s → HTTP %d: %s", cand.from, truncateStr(cand.id, 12), status, truncateStr(raw, 60))
		log.Printf("[影巢] ○ %s", lastErr)
	}
	if len(ids) == 0 {
		return false, fmt.Sprintf("未能从登录页解析出任何 action ID（页面 %d 字节，chunk %d 个）", len(warm), len(chunks))
	}
	return false, fmt.Sprintf("Server Action 登录未取到会话，尝试 %d 个候选均失败，最后: %s", len(ids), lastErr)
}

// hdhiveCheckLogin 校验登录态（GET /api/customer/user/info）
// hdhiveCheckLogin 校验登录态：优先用户信息接口（响应壳多版本兼容），
// 接口不存在时退化为个人页跳转探测（未登录访问 /user 会被重定向到 /login）
func (h *Handler) hdhiveCheckLogin(cfg *hdhiveCfg) (bool, map[string]any) {
	if cfg.Cookies["token"] == "" {
		return false, nil
	}
	// 1. 用户信息接口（success/state 两种壳、平铺数据都兼容）
	status, body, _, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/api/customer/user/info", "", nil)
	if err == nil && status < 400 && !hdhiveIsCFBlock(status, body) {
		var out struct {
			Success *bool          `json:"success"`
			State   *bool          `json:"state"`
			Data    map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(body), &out) == nil {
			ok := (out.Success != nil && *out.Success) || (out.State != nil && *out.State)
			if ok && out.Data != nil {
				return true, out.Data
			}
		}
		// 无壳平铺返回：出现明显用户字段即认为有效
		var flat map[string]any
		if json.Unmarshal([]byte(body), &flat) == nil {
			for _, k := range []string{"id", "user_id", "username", "nickname", "email"} {
				if _, exist := flat[k]; exist {
					return true, flat
				}
			}
		}
	}
	// 2. 个人页跳转探测：未登录会被站点重定向到 /login
	status, body, resp, err := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/user", "", nil)
	if err == nil && !hdhiveIsCFBlock(status, body) {
		loc := ""
		if resp != nil {
			loc = resp.Header.Get("Location")
		}
		if status < 400 && !strings.Contains(loc, "/login") {
			return true, nil
		}
	}
	return false, nil
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

	// 校验登录态并缓存账号信息。校验接口可能随站点改版不可用：
	// Server Action 已发下 token 会话即视为登录成功，不因校验失败而阻断
	ok, user := h.hdhiveCheckLogin(cfg)
	cfg.LoginAt = time.Now().Format("2006-01-02 15:04")
	if user != nil {
		b, _ := json.Marshal(user)
		cfg.UserJSON = string(b)
	}
	hdhiveSave(cfg)
	log.Printf("[影巢] ✓ 账号 %s 登录成功（会话校验 %v）", cfg.Username, ok)
	msg := "登录成功"
	if !ok {
		msg = "登录成功（会话已保存。账号信息接口暂不可用，不影响搜索与转存）"
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "user": user, "login_at": cfg.LoginAt, "verified": ok})
}

// HdhiveLogout POST /hdhive/logout → 清除会话
// HdhiveCheck GET /hdhive/check：实时校验登录态（前端页面加载后异步调用）
func (h *Handler) HdhiveCheck(c *gin.Context) {
	cfg := loadHdhiveCfg()
	loggedIn, user := h.hdhiveCheckLogin(cfg)
	if !loggedIn {
		// 校验接口可能改版：持有 token 会话即视为已登录（搜索/转存以实际会话为准）
		loggedIn = cfg.Cookies["token"] != ""
	}
	c.JSON(http.StatusOK, gin.H{"logged_in": loggedIn, "user": user, "login_at": cfg.LoginAt})
}

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
	// 即时返回：只看本地是否持有 token，不做网络探测（探测走 /hdhive/check 异步）
	loggedIn := cfg.Cookies["token"] != ""
	var user json.RawMessage
	if cfg.UserJSON != "" {
		user = json.RawMessage(cfg.UserJSON)
	}
	c.JSON(http.StatusOK, gin.H{
		"base_url":     cfg.BaseURL,
		"username":     cfg.Username,
		"password":     cfg.Password,
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

// hdhiveParseResourceListJSON 兼容多种响应壳的资源数组提取
func hdhiveParseResourceListJSON(body []byte) []map[string]any {
	var arr []map[string]any
	if json.Unmarshal(body, &arr) == nil && arr != nil {
		return arr
	}
	var obj struct {
		Success *bool           `json:"success"`
		State   *bool           `json:"state"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return nil
	}
	if obj.Data == nil {
		return nil
	}
	if json.Unmarshal(obj.Data, &arr) == nil && arr != nil {
		return arr
	}
	var inner map[string]any
	if json.Unmarshal(obj.Data, &inner) == nil {
		for _, k := range []string{"resources", "list", "items"} {
			if children, ok := inner[k].([]any); ok {
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
		}
	}
	return nil
}

// hdhiveResourceLess JSON 资源项推荐序：115 优先 → 免积分优先 → 4K/1080P → 标题
func hdhiveResourceLess(a, b map[string]any) bool {
	getStr := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	getInt := func(m map[string]any, keys ...string) int64 {
		for _, k := range keys {
			if f, ok := m[k].(float64); ok {
				return int64(f)
			}
		}
		return 0
	}
	panRank := func(m map[string]any) int {
		switch strings.ToLower(getStr(m, "pan_type", "drive_type", "netdisk", "cloud_type")) {
		case "115":
			return 0
		case "quark", "夸克":
			return 1
		}
		return 2
	}
	resRank := func(m map[string]any) int {
		for _, r := range []any{m["video_resolution"]} {
			if arr, ok := r.([]any); ok {
				for _, v := range arr {
					s := strings.ToUpper(fmt.Sprint(v))
					if strings.Contains(s, "4K") || strings.Contains(s, "2160") {
						return 0
					}
					if strings.Contains(s, "1080") {
						return 1
					}
				}
			}
		}
		return 2
	}
	if c := panRank(a) - panRank(b); c != 0 {
		return c < 0
	}
	pa, pb := getInt(a, "unlock_points", "points"), getInt(b, "unlock_points", "points")
	if (pa > 0) != (pb > 0) {
		return pa == 0
	}
	if c := resRank(a) - resRank(b); c != 0 {
		return c < 0
	}
	return getStr(a, "title", "name") < getStr(b, "title", "name")
}

// hdhiveFetchResourcesJSON 直连资源 JSON 接口（页面资源列表的数据源）。
// 返回 items/原始响应/是否要求请求签名
func (h *Handler) hdhiveFetchResourcesJSON(cfg *hdhiveCfg, mediaType, tmdbID string) ([]map[string]any, string, bool) {
	urls := []string{
		// /go-api/ 前缀：站点前端对该前缀用普通 fetch（不走 signedFetch 签名层）
		cfg.BaseURL + "/go-api/customer/resources?type=" + mediaType + "&tmdb_id=" + tmdbID,
		cfg.BaseURL + "/go-api/customer/resources?media_type=" + mediaType + "&tmdb_id=" + tmdbID,
		cfg.BaseURL + "/go-api/customer/resources?tmdb_id=" + tmdbID,
		// /api/customer/ 直连（有签名墙，通常 401 missing_signature）
		cfg.BaseURL + "/api/customer/resources?type=" + mediaType + "&tmdb_id=" + tmdbID,
		cfg.BaseURL + "/api/customer/resources?media_type=" + mediaType + "&tmdb_id=" + tmdbID,
		cfg.BaseURL + "/api/customer/resources?tmdb_id=" + tmdbID,
	}
	sigRequired := false
	var lastRaw string
	for _, u := range urls {
		status, body, resp, err := hdhiveDirect(cfg, "GET", u, "", nil)
		if err != nil {
			continue
		}
		if (status == 301 || status == 302 || status == 307 || status == 308) && resp != nil {
			if loc := resp.Header.Get("Location"); loc != "" {
				if !strings.HasPrefix(loc, "http") {
					loc = cfg.BaseURL + loc
				}
				status, body, _, err = hdhiveDirect(cfg, "GET", loc, "", nil)
				if err != nil {
					continue
				}
			}
		}
		lastRaw = fmt.Sprintf("HTTP %d: %s", status, truncateStr(body, 200))
		if status == 401 && (strings.Contains(body, "signature") || strings.Contains(body, "签名")) {
			sigRequired = true
			continue
		}
		if status >= 400 {
			continue
		}
		if items := hdhiveParseResourceListJSON([]byte(body)); items != nil {
			return items, body, false
		}
	}
	if sigRequired {
		log.Printf("[影巢] ○ 资源接口要求请求签名（missing_signature），直连 JSON 不可用")
	} else if lastRaw != "" {
		log.Printf("[影巢] ○ 资源 JSON 接口未解析出列表，最后响应: %s", lastRaw)
	}
	return nil, "", sigRequired
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
	// 优先直连页面背后的资源 JSON 接口
	if items, raw, sigRequired := h.hdhiveFetchResourcesJSON(cfg, mediaType, tmdbID); items != nil {
		sort.Slice(items, func(i, j int) bool { return hdhiveResourceLess(items[i], items[j]) })
		c.JSON(http.StatusOK, gin.H{"data": items, "source": "api"})
		return
	} else if sigRequired {
		log.Printf("[影巢] ✗ 资源接口要求请求签名（站点安全层），Go 直连无法复刻")
		c.JSON(http.StatusBadGateway, gin.H{"error": "影巢资源接口要求请求签名（站点安全层），暂时无法直连"})
		return
	} else if raw != "" {
		log.Printf("[影巢] ○ 资源 JSON 直连失败样例: %s", truncateStr(raw, 200))
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
	if len(cards) == 0 {
		refs := regexp.MustCompile(`/resource/115/([A-Za-z0-9_-]+)`).FindAllString(pageHTML, -1)
		log.Printf("[影巢] ○ 资源页解析 0 卡片（tmdb %s/%s）：页面 %d 字节，登录墙=%v，/resource/ 引用 %d 处",
			mediaType, tmdbID, len(pageHTML), hdhiveLoginWall(pageHTML), len(refs))
	}
	sort.Slice(cards, func(i, j int) bool { return hdhiveCardLess(cards[i], cards[j]) })
	c.JSON(http.StatusOK, gin.H{"data": cards})
}

// HdhiveDiagSign GET /hdhive/diag/sign
// 摘取含握手/签名的 JS chunk 关键代码段，用于评估 Go 侧复刻签名可行性
func (h *Handler) HdhiveDiagSign(c *gin.Context) {
	defer func() {
		if r := recover(); r != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("诊断异常: %v", r)})
		}
	}()
	cfg := loadHdhiveCfg()
	pageHTML, _, err := h.hdhivePage(cfg, "/login")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var chunks []string
	for _, m := range reHdhiveScriptSrc.FindAllStringSubmatch(pageHTML, 40) {
		src := m[1]
		if !strings.HasPrefix(src, "http") {
			src = cfg.BaseURL + src
		}
		chunks = append(chunks, src)
	}
	type excerpt struct {
		URL  string   `json:"url"`
		Hits []string `json:"hits"`
	}
	keywords := []string{"session/handshake", "signedFetch", "missing_signature", "action-proof", "signature", "wasm"}
	var out []excerpt
	for _, u := range chunks {
		_, body, _, err := hdhiveDirect(cfg, "GET", u, "", map[string]string{"Accept": "*/*"})
		if err != nil || len(body) == 0 {
			continue
		}
		lower := strings.ToLower(body)
		e := excerpt{URL: u}
		for _, kw := range keywords {
			pos := 0
			for n := 0; n < 3; n++ {
				idx := strings.Index(lower[pos:], strings.ToLower(kw))
				if idx < 0 {
					break
				}
				at := pos + idx
				start, end := at-350, at+500
				if start < 0 {
					start = 0
				}
				if end > len(body) {
					end = len(body)
				}
				e.Hits = append(e.Hits, fmt.Sprintf("[%s] …%s…", kw, strings.ReplaceAll(body[start:end], "\n", " ")))
				pos = at + len(kw)
			}
		}
		if len(e.Hits) > 0 {
			out = append(out, e)
		}
	}
	// 握手接口本体：不带参数 GET 一次，看它要求什么输入
	hsStatus, hsBody, _, hsErr := hdhiveDirect(cfg, "GET", cfg.BaseURL+"/api/public/security/session/handshake", "", nil)
	hs := gin.H{"status": hsStatus, "head": truncateStr(hsBody, 400)}
	if hsErr != nil {
		hs["error"] = sanitizeWecomErr(hsErr)
	}
	c.JSON(http.StatusOK, gin.H{"chunks": chunks, "sign_findings": out, "handshake": hs})
}

// HdhiveDiag GET /hdhive/diag?path=/tmdb/movie/1930
// 影巢页面为前端渲染，资源列表由浏览器调内部接口加载。此接口抓取
// 页面与其引用的 JS chunk，挖出隐藏的 API 端点与 action ID 供适配。
func (h *Handler) HdhiveDiag(c *gin.Context) {
	defer func() {
		if r := recover(); r != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("诊断过程异常: %v", r)})
		}
	}()
	cfg := loadHdhiveCfg()
	path := c.DefaultQuery("path", "/tmdb/movie/1930")
	pageHTML, finalURL, err := h.hdhivePage(cfg, path)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	title := ""
	if m := regexp.MustCompile(`<title>([^<]{0,120})`).FindStringSubmatch(pageHTML); m != nil {
		title = m[1]
	}
	buildID := ""
	if m := regexp.MustCompile(`"buildId":"([^"]+)"`).FindStringSubmatch(pageHTML); m != nil {
		buildID = m[1]
	}
	apiRefs := map[string]bool{}
	for _, m := range regexp.MustCompile(`/api/[a-zA-Z0-9/_.?=&-]+`).FindAllString(pageHTML, -1) {
		apiRefs[strings.TrimSuffix(m, "&")] = true
	}
	var chunks []string
	for _, m := range reHdhiveScriptSrc.FindAllStringSubmatch(pageHTML, 40) {
		src := m[1]
		if !strings.HasPrefix(src, "http") {
			src = cfg.BaseURL + src
		}
		chunks = append(chunks, src)
	}
	// 扫描全部 chunk：API 路径、action ID、关键词上下文摘要
	type chunkFinding struct {
		URL     string   `json:"url"`
		Status  string   `json:"status,omitempty"`
		Size    int      `json:"size,omitempty"`
		APIs    []string `json:"apis,omitempty"`
		Actions []string `json:"actions,omitempty"`
		Excerpts []string `json:"excerpts,omitempty"`
	}
	excerptAround := func(body, kw string) string {
		idx := strings.Index(strings.ToLower(body), strings.ToLower(kw))
		if idx < 0 {
			return ""
		}
		start, end := idx-150, idx+260
		if start < 0 {
			start = 0
		}
		if end > len(body) {
			end = len(body)
		}
		return fmt.Sprintf("[%s] …%s…", kw, strings.ReplaceAll(body[start:end], "\n", " "))
	}
	var findings []chunkFinding
	keywords := []string{"resource", "tmdb", "customer", "fetch("}
	for _, src := range chunks {
		_, body, _, err := hdhiveDirect(cfg, "GET", src, "", map[string]string{"Accept": "*/*"})
		if err != nil {
			findings = append(findings, chunkFinding{URL: src, Status: "fetch-err: " + sanitizeWecomErr(err)})
			continue
		}
		f := chunkFinding{URL: src, Size: len(body)}
		apiSet := map[string]bool{}
		for _, m := range regexp.MustCompile(`/api/[a-zA-Z0-9/_.?=&-]+`).FindAllString(body, -1) {
			apiSet[strings.TrimSuffix(m, "&")] = true
		}
		for k := range apiSet {
			f.APIs = append(f.APIs, k)
		}
		for _, m := range regexp.MustCompile(`"([a-f0-9]{40,64})"[\s\S]{0,300}?"(login|resource|checkin|sign)"`).FindAllStringSubmatch(body, -1) {
			f.Actions = append(f.Actions, m[1]+"("+m[2]+")")
		}
		seen := map[string]bool{}
		for _, kw := range keywords {
			if e := excerptAround(body, kw); e != "" && !seen[e] {
				seen[e] = true
				f.Excerpts = append(f.Excerpts, e)
			}
		}
		if len(f.APIs) > 0 || len(f.Actions) > 0 || len(f.Excerpts) > 0 {
			findings = append(findings, f)
		}
	}

	// 页面专属 chunk 藏在 flight 数据里（script 标签没有）：挖出来扫描 resources 调用参数
	var appChunks []string
	appSeen := map[string]bool{}
	for _, m := range regexp.MustCompile(`static/chunks/app/[^"'\\]+\.js`).FindAllString(pageHTML, -1) {
		full := cfg.BaseURL + "/_next/" + m
		if !appSeen[full] {
			appSeen[full] = true
			appChunks = append(appChunks, full)
		}
	}
	var appFindings []chunkFinding
	for i, src := range appChunks {
		if i >= 6 {
			break
		}
		_, body, _, err := hdhiveDirect(cfg, "GET", src, "", map[string]string{"Accept": "*/*"})
		if err != nil {
			continue
		}
		f := chunkFinding{URL: src, Size: len(body)}
		apiSet := map[string]bool{}
		for _, m := range regexp.MustCompile(`/api/[a-zA-Z0-9/_.?=&${}-]+`).FindAllString(body, -1) {
			apiSet[strings.TrimSuffix(m, "&")] = true
		}
		for k := range apiSet {
			f.APIs = append(f.APIs, k)
		}
		seen := map[string]bool{}
		for _, kw := range []string{"resources", "tmdb_id", "media_type"} {
			lower := strings.ToLower(body)
			for pos, n := 0, 0; n < 3; n++ {
				idx := strings.Index(lower[pos:], strings.ToLower(kw))
				if idx < 0 {
					break
				}
				at := pos + idx
				start, end := at-120, at+280
				if start < 0 {
					start = 0
				}
				if end > len(body) {
					end = len(body)
				}
				e := fmt.Sprintf("[%s#%d] …%s…", kw, n, strings.ReplaceAll(body[start:end], "\n", " "))
				if !seen[e] {
					seen[e] = true
					f.Excerpts = append(f.Excerpts, e)
				}
				pos = at + len(kw)
			}
		}
		appFindings = append(appFindings, f)
	}

	// 端点试探：资源列表与当前用户（候选参数组合逐一探，记录状态与响应开头）
	type probeResult struct {
		URL    string `json:"url"`
		Status int    `json:"status"`
		Head   string `json:"head"`
	}
	var probes []probeResult
	probe := func(u string) {
		st, body, _, err := hdhiveDirect(cfg, "GET", u, "", nil)
		if err != nil {
			probes = append(probes, probeResult{URL: u, Status: -1, Head: sanitizeWecomErr(err)})
			return
		}
		probes = append(probes, probeResult{URL: u, Status: st, Head: truncateStr(body, 240)})
	}
	movieID := path[strings.LastIndex(path, "/")+1:]
	probe(cfg.BaseURL + "/api/customer/user/current")
	probe(cfg.BaseURL + "/api/customer/resources/?tmdb_id=" + movieID)
	probe(cfg.BaseURL + "/api/customer/resources/?tmdb_id=" + movieID + "&type=movie")
	probe(cfg.BaseURL + "/api/customer/resources/?media_type=movie&tmdb_id=" + movieID)
	probe(cfg.BaseURL + "/api/customer/resources/movie/" + movieID)
	probe(cfg.BaseURL + "/go-api/customer/resources/?tmdb_id=" + movieID)
	c.JSON(http.StatusOK, gin.H{
		"path":           path,
		"final_url":      finalURL,
		"page_len":       len(pageHTML),
		"login_wall":     hdhiveLoginWall(pageHTML),
		"title":          title,
		"build_id":       buildID,
		"api_refs":       apiRefs,
		"resource_refs":  len(regexp.MustCompile(`/resource/115/`).FindAllString(pageHTML, -1)),
		"chunk_count":    len(chunks),
		"chunks":         chunks,
		"chunk_findings": findings,
		"app_chunks":     appChunks,
		"app_findings":   appFindings,
		"probes":         probes,
	})
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
