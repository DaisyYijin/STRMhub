package api

// 影巢「浏览器渲染」通道：
// 站点对 /api/customer/* 全量启用请求签名（握手 + WASM 计算，服务端随时轮换
// 密钥表），Go 直连无法复刻。改为在服务端跑无头 Chromium，注入影巢登录
// Cookie 后打开资源页 —— 握手/签名/CF 全部由站点自身 JS 完成，我们只截获
// 页面发出的资源 JSON 响应（DOM 解析兜底）。

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// hdhiveChromePath 探测可用的 Chrome/Chromium 可执行文件（容器内为 alpine chromium）
func hdhiveChromePath() string {
	candidates := []string{os.Getenv("HDHIVE_CHROME"), "/usr/bin/chromium-browser", "/usr/bin/chromium", "/usr/bin/google-chrome-stable", "/usr/bin/google-chrome"}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if abs, err := exec.LookPath(p); err == nil {
			return abs
		}
	}
	return ""
}

// hdhiveCookieDomain 从 BaseURL 取注册域名（去掉端口）
func hdhiveCookieDomain(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "hdhive.com"
	}
	return u.Hostname()
}

// hdhiveReqLog 浏览器会话中观察到的请求（诊断用）
type hdhiveReqLog struct {
	Status int    `json:"status"`
	URL    string `json:"url"`
}

// hdhiveBrowserResult 浏览器渲染结果
type hdhiveBrowserResult struct {
	APIBody string          `json:"-"`
	HTML    string          `json:"-"`
	Reqs    []hdhiveReqLog  `json:"requests"`
	Text    string          `json:"page_text"`
}

// hdhiveBrowserFetch 用无头浏览器打开 pagePath，注入登录 Cookie，
// 轮询等待 waitExpr（JS 表达式）为真或超时。
func hdhiveBrowserFetch(cfg *hdhiveCfg, pagePath, apiPath, waitExpr string, wait time.Duration) (*hdhiveBrowserResult, error) {
	exe := hdhiveChromePath()
	if exe == "" {
		return nil, fmt.Errorf("服务器缺少 Chromium（镜像内置 alpine chromium；本机开发可设 HDHIVE_CHROME 指向 Chrome 可执行文件）")
	}
	ua := cfg.UA
	if ua == "" {
		ua = hdhiveDefaultUA
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.ExecPath(exe),
		chromedp.UserAgent(ua),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("window-size", "1440,900"),
		chromedp.Flag("lang", "zh-CN"),
		// 反无头检测：站点安全层会检查自动化/无头指纹（navigator.webdriver 等）
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("accept-lang", "zh-CN,zh;q=0.9,en;q=0.8"),
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	defer cancelCtx()
	ctx, cancelTo := context.WithTimeout(ctx, wait+25*time.Second)
	defer cancelTo()

	domain := hdhiveCookieDomain(cfg.BaseURL)

	var (
		mu       sync.Mutex
		lastRID  network.RequestID
		finished = make(chan network.RequestID, 8)
		reqLog   []hdhiveReqLog
	)
	captureBody := func(rid network.RequestID) string {
		var body []byte
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
			var err error
			body, err = network.GetResponseBody(rid).Do(c)
			return err
		})); err != nil || len(body) == 0 {
			return ""
		}
		return string(body)
	}

	// 监听页面自己发出的资源接口请求（签名由站点 JS 完成，直接拿响应结果）
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *network.EventResponseReceived:
			if e.Response == nil {
				return
			}
			mu.Lock()
			if strings.Contains(e.Response.URL, apiPath) {
				lastRID = e.RequestID
			}
			// 记录站内接口请求（诊断 0 卡片时定位数据源）
			u := e.Response.URL
			if (strings.Contains(u, "/api/") || strings.Contains(u, "/wasm/") || strings.Contains(u, "/go-api/")) && len(reqLog) < 60 {
				reqLog = append(reqLog, hdhiveReqLog{Status: int(e.Response.Status), URL: u})
			}
			mu.Unlock()
		case *network.EventLoadingFinished:
			mu.Lock()
			mine := e.RequestID == lastRID
			mu.Unlock()
			if mine {
				select {
				case finished <- e.RequestID:
				default:
				}
			}
		}
	})

	var html string
	apiHit := ""
	waitCard := func() bool {
		var n int64
		if err := chromedp.Run(ctx, chromedp.Evaluate("("+waitExpr+")?1:0", &n)); err != nil {
			return false
		}
		return n > 0
	}
	runErr := func() error {
		log.Printf("[影巢] ▶ 浏览器启动 %s", exe)
		t0 := time.Now()
		if err := chromedp.Run(ctx, network.Enable()); err != nil {
			return fmt.Errorf("network.Enable: %w", err)
		}
		// 无头指纹伪装（document start 注入，先于站点 JS 执行）
		if _, err := page.AddScriptToEvaluateOnNewDocument(hdhiveStealthJS).Do(ctx); err != nil {
			return fmt.Errorf("注入伪装脚本: %w", err)
		}
		// sec-ch-ua 客户端提示头伪装成正常 Chrome（无头模式默认带 HeadlessChrome 品牌）
		if err := chromedp.Run(ctx, network.SetExtraHTTPHeaders(network.Headers{
			"sec-ch-ua":         "\"Chromium\";v=\"131\", \"Not_A Brand\";v=\"24\"",
			"sec-ch-ua-mobile":  "?0",
			"sec-ch-ua-platform": "\"Windows\"",
		})); err != nil {
			return fmt.Errorf("设置客户端提示头: %w", err)
		}
		log.Printf("[影巢] ▶ 浏览器就绪（%s），注入 Cookie %d 条", time.Since(t0).Round(time.Millisecond), len(cfg.Cookies))
		if len(cfg.Cookies) > 0 {
			err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
				for k, v := range cfg.Cookies {
					if k == "" || v == "" {
						continue
					}
					if err := network.SetCookie(k, v).WithDomain(domain).WithPath("/").WithSecure(true).Do(c); err != nil {
						return fmt.Errorf("注入 Cookie %s: %w", k, err)
					}
				}
				return nil
			}))
			if err != nil {
				return err
			}
		}
		target := strings.TrimRight(cfg.BaseURL, "/") + pagePath
		if err := chromedp.Run(ctx, chromedp.Navigate(target)); err != nil {
			return fmt.Errorf("打开 %s: %w", target, err)
		}
		log.Printf("[影巢] ▶ 页面已打开 %s，等待渲染…", pagePath)
		deadline := time.Now().Add(wait)
		lastTick := time.Now()
		for time.Now().Before(deadline) {
			select {
			case rid := <-finished:
				if body := captureBody(rid); body != "" {
					log.Printf("[影巢] ▶ 截获接口响应 %d 字节", len(body))
					apiHit = body
				}
			default:
			}
			if waitCard() {
				// DOM 就绪：再给接口响应一点落地时间
				select {
				case rid := <-finished:
					if body := captureBody(rid); body != "" {
						apiHit = body
					}
				case <-time.After(1500 * time.Millisecond):
				}
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(600 * time.Millisecond):
			}
			if time.Since(lastTick) >= 5*time.Second {
				lastTick = time.Now()
				log.Printf("[影巢] ▶ 渲染等待中 %s/%s，已截获 %d 字节", time.Since(deadline.Add(-wait)).Round(time.Second), wait, len(apiHit))
			}
		}
		if err := chromedp.Run(ctx, chromedp.OuterHTML("html", &html)); err != nil {
			return fmt.Errorf("读取渲染结果: %w", err)
		}
		var fp string
		_ = chromedp.Run(ctx, chromedp.Evaluate(
			`'webdriver='+navigator.webdriver+' plugins='+(navigator.plugins?navigator.plugins.length:'x')+' ua='+navigator.userAgent.slice(0,90)`, &fp))
		log.Printf("[影巢] ▶ 渲染完成：HTML %d 字节，接口响应 %d 字节，指纹[%s]", len(html), len(apiHit), fp)
		return nil
	}()
	if runErr != nil {
		return nil, runErr
	}
	var text string
	_ = chromedp.Run(ctx, chromedp.Evaluate("document.body ? document.body.innerText : ''", &text))
	mu.Lock()
	defer mu.Unlock()
	return &hdhiveBrowserResult{APIBody: apiHit, HTML: html, Reqs: reqLog, Text: truncateStr(text, 1200)}, nil
}

// hdhiveStealthJS 抹掉无头 Chromium 的常见指纹（webdriver/插件/语言/WebGL 渲染器），
// 站点安全层在 JS 层做浏览器环境检测
const hdhiveStealthJS = `
Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
if (!window.chrome) { window.chrome = {runtime: {}, loadTimes: () => ({}), csi: () => ({})}; }
Object.defineProperty(navigator, 'languages', {get: () => ['zh-CN', 'zh', 'en']});
try {
  Object.defineProperty(navigator, 'plugins', {get: () => {
    const mk = (n) => ({name: n, filename: 'internal-pdf-viewer', description: 'Portable Document Format', length: 1});
    return [mk('Chrome PDF Viewer'), mk('Chromium PDF Viewer'), mk('Microsoft Edge PDF Viewer'), mk('WebKit built-in PDF')];
  }});
} catch (e) {}
try {
  const origQuery = navigator.permissions.query.bind(navigator.permissions);
  navigator.permissions.query = (p) => p && p.name === 'notifications'
    ? Promise.resolve({state: Notification.permission}) : origQuery(p);
} catch (e) {}
try {
  const proto = WebGLRenderingContext.prototype, orig = proto.getParameter;
  proto.getParameter = function(p) {
    if (p === 37445) return 'Intel Inc.';
    if (p === 37446) return 'Intel Iris OpenGL Engine';
    return orig.apply(this, [p]);
  };
} catch (e) {}
`
