package api

// ==================== 观影（GuanYing）资源站接入 ====================
//
// 观影是服务端渲染的网盘资源索引站，全站两层门槛：
//  1. PoW 反爬：未携带通行 Cookie 的请求会拿到「Loading...」中间页，
//     需 GET /res/pow 取挑战 {N,x,t}，计算 y = x^(2^t) mod N（t 次平方），
//     POST 回 /res/pow 换取放行 Cookie（RSW 工作量证明，Go big.Int 可解）
//  2. 站内登录：搜索/详情仅登录用户可见，表单 POST /user/login
//     （username/password/cookietime；服务端可能校验验证码，失败时
//     请改用浏览器 Cookie 导入）
//
// 集成方式：账号密码登录（Cookie 持久化，站点会话过期自动重登）→
// 搜索页/详情页 HTML 里按网盘域名提取链接 → 115 链接一键转存入库。
// 站点常换域名（gying.in 已失效），base_url 可配置。

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const gyDefaultBase = "https://www.xn--wcv59z.com"

var gyUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// gyCfg 观影配置（setting key "guanying"）
type gyCfg struct {
	BaseURL  string            `json:"base_url"`
	Username string            `json:"username"`
	Password string            `json:"password"`
	Cookies  map[string]string `json:"cookies"` // 站点会话 + PoW 放行 Cookie
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
	if cfg.Cookies == nil {
		cfg.Cookies = map[string]string{}
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

// ==================== HTTP 客户端（Cookie Jar + PoW 自动过验证） ====================

// gyJar 极简 CookieJar：单站点，名字→值
type gyJar struct {
	mu sync.Mutex
	m  map[string]string
}

func (j *gyJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		j.m[c.Name] = c.Value
	}
}

func (j *gyJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.m))
	for k, v := range j.m {
		out = append(out, &http.Cookie{Name: k, Value: v})
	}
	return out
}

// gyClient 带 Jar 的 HTTP 客户端
func gyClient(jar *gyJar) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向过多")
			}
			return nil
		},
	}
}

var (
	reGyYear   = regexp.MustCompile(`(19|20)\d{2}`)
	reHTMLText = regexp.MustCompile(`<[^>]+>`)
)

// gyPanRules 网盘链接识别（域名分类）
var gyPanRules = []struct {
	pattern *regexp.Regexp
	pan     string
}{
	{regexp.MustCompile(`(?:115\.com|115cdn\.com|anxia\.com)/s/[a-zA-Z0-9_-]+`), "115"},
	{regexp.MustCompile(`pan\.quark\.cn/s/[a-zA-Z0-9]+`), "quark"},
	{regexp.MustCompile(`pan\.baidu\.com/s/[a-zA-Z0-9_-]+`), "baidu"},
	{regexp.MustCompile(`pan\.xunlei\.com/s/[a-zA-Z0-9]+`), "xunlei"},
	{regexp.MustCompile(`drive\.uc\.cn/s/[a-zA-Z0-9]+`), "uc"},
	{regexp.MustCompile(`(?:alipan|aliyundrive)\.com/s/[a-zA-Z0-9]+`), "ali"},
	{regexp.MustCompile(`123(?:pan|684|865|912)\.(?:com|cn)/s/[a-zA-Z0-9_-]+`), "123"},
	{regexp.MustCompile(`cloud\.189\.cn/[tw]/[a-zA-Z0-9]+`), "tianyi"},
	{regexp.MustCompile(`magnet:\?xt=urn:btih:[a-zA-Z0-9]{8,}`), "magnet"},
}

// gyDetectPan 按域名识别网盘类型，非网盘链接返回空
func gyDetectPan(s string) string {
	for _, r := range gyPanRules {
		if r.pattern.MatchString(s) {
			return r.pan
		}
	}
	return ""
}

// gyIsPow 判断响应是否为 PoW 验证中间页
func gyIsPow(body string) bool {
	return strings.Contains(body, "powSolve") || strings.Contains(body, "pow.core")
}

// gyIsNoLogin 判断页面是否提示需要登录（访客标记：nologin 脚本 / 受限标题）
func gyIsNoLogin(body string) bool {
	return strings.Contains(body, "nologin-e02c") || strings.Contains(body, "请登录后继续") ||
		strings.Contains(body, "未登录，访问受限")
}

// gyPow 过 PoW 验证：取挑战→反复平算→提交换 Cookie
func gyPow(client *http.Client, base string) error {
	// 挑战与提交的 UA 必须和后续业务请求完全一致——站点把放行凭证绑定到
	// 浏览器指纹（UA 不一致时业务请求会报「浏览器验证已过期」）
	req, err := http.NewRequest(http.MethodGet, base+"/res/pow", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", gyUA)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("获取 PoW 挑战失败: %s", sanitizeWecomErr(err))
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	var ch struct {
		N string `json:"N"`
		X string `json:"x"`
		T int    `json:"t"`
	}
	if json.Unmarshal(raw, &ch) != nil || ch.N == "" || ch.X == "" || ch.T <= 0 {
		return fmt.Errorf("PoW 挑战解析失败: %s", truncateStr(string(raw), 100))
	}
	n, ok1 := new(big.Int).SetString(ch.N, 16)
	y, ok2 := new(big.Int).SetString(ch.X, 16)
	if !ok1 || !ok2 {
		return fmt.Errorf("PoW 挑战数值非法")
	}
	// y = x^(2^t) mod N：等价于连续 t 次平方取模（RSW 谜题，无法并行加速）
	for i := 0; i < ch.T; i++ {
		y.Mul(y, y)
		y.Mod(y, n)
	}

	form := url.Values{"y": {y.Text(16)}}
	req2, err := http.NewRequest(http.MethodPost, base+"/res/pow", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req2.Header.Set("User-Agent", gyUA)
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := client.Do(req2)
	if err != nil {
		return fmt.Errorf("提交 PoW 结果失败: %s", sanitizeWecomErr(err))
	}
	raw2, _ := io.ReadAll(io.LimitReader(resp2.Body, 16<<10))
	resp2.Body.Close()
	var vr struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(raw2, &vr) != nil || !vr.Success {
		return fmt.Errorf("PoW 验证被拒: %s", truncateStr(string(raw2), 100))
	}
	return nil
}

// gyDo 带自动过 PoW 的页面请求：返回响应正文（字符串）
func gyDo(client *http.Client, base, method, path string, form url.Values) (string, error) {
	return gyDoReq(client, base, method, path, form, nil)
}

// gyDoReq 同 gyDo，允许附加请求头
func gyDoReq(client *http.Client, base, method, path string, form url.Values, hdrs map[string]string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var bodyReader io.Reader
		if form != nil {
			bodyReader = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequest(method, base+path, bodyReader)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", gyUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		body := string(raw)
		if gyIsPow(body) && attempt < 2 {
			if err := gyPow(client, base); err != nil {
				return "", err
			}
			continue // 过完验证重放原请求
		}
		return body, nil
	}
	return "", fmt.Errorf("PoW 验证后请求仍未通过")
}

// gyLoggedIn 探测当前会话是否已登录。
// 访客页面的服务端标记：<title>未登录，访问受限</title> + nologin 脚本；
// （导航里的退出链接是前端 JS 渲染的，服务端 HTML 里没有，不能用作判定）
func gyLoggedIn(client *http.Client, base string) (bool, error) {
	body, err := gyDo(client, base, http.MethodGet, "/", nil)
	if err != nil {
		return false, err
	}
	return !gyIsNoLogin(body), nil
}

// gyLoginOnce 账号密码登录。
// 真实请求格式（从站点前端 loginm.post 逆向）：表单
// code=<验证码可空>&siteid=1&dosubmit=1&username=&password=&cookietime=10506240，
// 响应为 JSON {code, msg}（非 200=失败，msg 为站点原文，如「账号不存在」）
func gyLoginOnce(client *http.Client, base, username, password string) error {
	form := url.Values{
		"code":       {""},
		"siteid":     {"1"},
		"dosubmit":   {"1"},
		"username":   {username},
		"password":   {password},
		"cookietime": {"10506240"},
	}
	hdrs := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	body, err := gyDoReq(client, base, http.MethodPost, "/user/login", form, hdrs)
	if err != nil {
		return err
	}
	// 成功：JSON {code:200,...}；失败：code=403/其他，msg 为站点原文
	var jr struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal([]byte(body), &jr) == nil && jr.Msg != "" {
		if jr.Code == 200 || jr.Code == 0 {
			return nil
		}
		// 验证过期类报错 → 重过一次 PoW 再试登录
		if strings.Contains(jr.Msg, "验证") || strings.Contains(body, "powSolve") {
			if err := gyPow(client, base); err == nil {
				body, err = gyDoReq(client, base, http.MethodPost, "/user/login", form, hdrs)
				if err != nil {
					return err
				}
				if json.Unmarshal([]byte(body), &jr) == nil && jr.Msg != "" {
					if jr.Code == 200 || jr.Code == 0 {
						return nil
					}
					return fmt.Errorf("%s", jr.Msg)
				}
			}
		}
		return fmt.Errorf("%s", jr.Msg)
	}
	// 非 JSON 响应（异常情况）→ 回退会话探测
	ok, err := gyLoggedIn(client, base)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("登录未通过（站点响应异常）")
	}
	return nil
}

// gyEnsureClient 载入 Cookie 构建客户端
func gyEnsureClient(cfg *gyCfg) *http.Client {
	jar := &gyJar{m: map[string]string{}}
	for k, v := range cfg.Cookies {
		jar.m[k] = v
	}
	return gyClient(jar)
}

// ==================== 链接提取 ====================

// gyLink 一条网盘链接
type gyLink struct {
	URL  string `json:"url"`
	Code string `json:"code"`
	Pan  string `json:"pan"`
}

var (
	reGyCode  = regexp.MustCompile(`(?:提取码|访问码|密码|pwd|pass(?:word)?|code)[=:：\s]*([A-Za-z0-9]{4})`)
	reScript  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	reAnchor  = regexp.MustCompile(`(?is)<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reTitle   = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	reTagNFmt = regexp.MustCompile(`\s+`)
)

// gyHTMLUnescape 基本实体反转义
func gyHTMLUnescape(s string) string {
	r := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'", "&apos;", "'")
	return r.Replace(s)
}

// gyExtractAnchors 从页面提取内链条目（href, 文本），过滤导航
func gyExtractAnchors(body string) []gin.H {
	clean := reScript.ReplaceAllString(body, "")
	out := []gin.H{}
	seen := map[string]bool{}
	for _, m := range reAnchor.FindAllStringSubmatch(clean, -1) {
		href := strings.TrimSpace(m[1])
		if href == "" || href[0] != '/' {
			continue
		}
		for _, bad := range []string{"/user", "/search", "/res/", "/help", "/about", "favicon"} {
			if strings.HasPrefix(href, bad) {
				href = ""
				break
			}
		}
		if href == "" || seen[href] {
			continue
		}
		text := reTagNFmt.ReplaceAllString(strings.TrimSpace(gyHTMLUnescape(reHTMLText.ReplaceAllString(m[2], " "))), " ")
		if len([]rune(text)) < 2 || len([]rune(text)) > 80 {
			continue
		}
		seen[href] = true
		out = append(out, gin.H{"href": href, "title": text})
		if len(out) >= 30 {
			break
		}
	}
	return out
}

// gyExtractLinks 从 HTML 里提取全部网盘链接（href + 正文），带邻近提取码
func gyExtractLinks(body string) []gyLink {
	clean := reScript.ReplaceAllString(body, "")
	clean = gyHTMLUnescape(clean)
	links := []gyLink{}
	seen := map[string]bool{}
	for _, r := range gyPanRules {
		for _, loc := range r.pattern.FindAllStringIndex(clean, -1) {
			u := clean[loc[0]:loc[1]]
			// 站点文本里常省略协议头，归一化成完整链接（磁力本身无协议头）
			if r.pan != "magnet" && !strings.HasPrefix(u, "http") {
				u = "https://" + u
			}
			if seen[u] {
				continue
			}
			seen[u] = true
			link := gyLink{URL: u, Pan: r.pan}
			// 提取码通常跟在链接附近
			window := clean[loc[1]:]
			if len(window) > 200 {
				window = window[:200]
			}
			if m := reGyCode.FindStringSubmatch(window); m != nil {
				link.Code = m[1]
			}
			links = append(links, link)
			if len(links) >= 40 {
				return links
			}
		}
	}
	return links
}

// ==================== HTTP 处理器 ====================

// GyGetConfig GET /guanying/config（回填用；密码原样回传便于编辑，站点凭据非高敏）
func (h *Handler) GyGetConfig(c *gin.Context) {
	cfg := loadGyCfg()
	loggedIn := false
	if len(cfg.Cookies) > 0 {
		client := gyEnsureClient(cfg)
		loggedIn, _ = gyLoggedIn(client, cfg.BaseURL)
	}
	c.JSON(http.StatusOK, gin.H{
		"base_url":  cfg.BaseURL,
		"username":  cfg.Username,
		"password":  cfg.Password,
		"logged_in": loggedIn,
		"has_cookies": len(cfg.Cookies) > 0,
	})
}

// GyLogin POST /guanying/login {base_url?, username, password}
// 保存凭据 → 过 PoW → 表单登录 → 会话 Cookie 持久化
func (h *Handler) GyLogin(c *gin.Context) {
	var req struct {
		BaseURL  string `json:"base_url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写账号和密码"})
		return
	}
	cfg := loadGyCfg()
	if strings.TrimSpace(req.BaseURL) != "" {
		cfg.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	}
	cfg.Username = strings.TrimSpace(req.Username)
	cfg.Password = req.Password

	jar := &gyJar{m: map[string]string{}}
	client := gyClient(jar)
	if err := gyLoginOnce(client, cfg.BaseURL, cfg.Username, cfg.Password); err != nil {
		log.Printf("[观影] ✗ 登录失败: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	cfg.Cookies = jar.m
	if err := saveGyCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存会话失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ✓ 登录成功（%s）", cfg.Username)
	c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
}

// GyLogout POST /guanying/logout → 清空会话
func (h *Handler) GyLogout(c *gin.Context) {
	cfg := loadGyCfg()
	cfg.Cookies = map[string]string{}
	if err := saveGyCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ○ 已退出登录")
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

// gyAutoLogin Cookie 失效时用存储的凭据自动重登
func gyAutoLogin(cfg *gyCfg) error {
	if cfg.Username == "" || cfg.Password == "" {
		return fmt.Errorf("未保存观影账号，请先登录")
	}
	jar := &gyJar{m: map[string]string{}}
	client := gyClient(jar)
	if err := gyLoginOnce(client, cfg.BaseURL, cfg.Username, cfg.Password); err != nil {
		return err
	}
	cfg.Cookies = jar.m
	return saveGyCfg(cfg)
}

// GySearch GET /guanying/search?query=xxx → 解析搜索结果页条目
func (h *Handler) GySearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("query"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入影视名称"})
		return
	}
	cfg := loadGyCfg()
	client := gyEnsureClient(cfg)
	body, err := gyDo(client, cfg.BaseURL, http.MethodGet, "/search?q="+url.QueryEscape(q), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "连接观影失败: " + err.Error() + "（站点换域名时请更新站点地址）"})
		return
	}
	// 会话失效 → 自动重登一次
	if gyIsNoLogin(body) {
		if err := gyAutoLogin(cfg); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "观影需要登录（站点设置里填写账号密码，或登录已失效）: " + err.Error()})
			return
		}
		client = gyEnsureClient(cfg)
		body, err = gyDo(client, cfg.BaseURL, http.MethodGet, "/search?q="+url.QueryEscape(q), nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "重登后连接观影失败: " + err.Error()})
			return
		}
		if gyIsNoLogin(body) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "重登后仍未通过（账号可能被封或需要验证码），请检查账号"})
			return
		}
	}
	items := gyExtractAnchors(body)
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// GyResources GET /guanying/resources?path=/xxx → 详情页提取网盘链接
func (h *Handler) GyResources(c *gin.Context) {
	p := strings.TrimSpace(c.Query("path"))
	if p == "" || !strings.HasPrefix(p, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少条目路径"})
		return
	}
	cfg := loadGyCfg()
	client := gyEnsureClient(cfg)
	body, err := gyDo(client, cfg.BaseURL, http.MethodGet, p, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "连接观影失败: " + err.Error()})
		return
	}
	if gyIsNoLogin(body) {
		if err := gyAutoLogin(cfg); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已失效: " + err.Error()})
			return
		}
		client = gyEnsureClient(cfg)
		body, err = gyDo(client, cfg.BaseURL, http.MethodGet, p, nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "重登后连接观影失败: " + err.Error()})
			return
		}
	}
	links := gyExtractLinks(body)
	title := ""
	if m := reTitle.FindStringSubmatch(body); m != nil {
		title = gyHTMLUnescape(strings.TrimSpace(m[1]))
	}
	c.JSON(http.StatusOK, gin.H{"data": links, "title": title})
}

// GyTransfer POST /guanying/transfer {url, code} → 115 分享自动转存
func (h *Handler) GyTransfer(c *gin.Context) {
	var req struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少链接"})
		return
	}
	if !is115ShareLink(req.URL) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 115 分享链接的自动转存，其他网盘请复制链接手动转存"})
		return
	}
	msg, ok, fail, err := h.shareReceiveCore(req.URL, req.Code, "", true)
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
