package api

// 影巢「浏览器渲染」通道：
// 站点对 /api/customer/* 全量启用请求签名（握手 + WASM 计算，服务端随时轮换
// 密钥表），Go 直连无法复刻。改为在服务端跑无头 Chromium，注入影巢登录
// Cookie 后打开资源页 —— 握手/签名/CF 全部由站点自身 JS 完成，我们只截获
// 页面发出的资源 JSON 响应（DOM 解析兜底）。

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/cdproto/network"
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

// hdhiveBrowserFetch 用无头浏览器打开 pagePath，注入登录 Cookie，
// 轮询等待 waitExpr（JS 表达式）为真或超时；
// 返回（截获的 apiPath 接口响应体，最终页面 HTML）。
func hdhiveBrowserFetch(cfg *hdhiveCfg, pagePath, apiPath, waitExpr string, wait time.Duration) (string, string, error) {
	exe := hdhiveChromePath()
	if exe == "" {
		return "", "", fmt.Errorf("服务器缺少 Chromium（镜像内置 alpine chromium；本机开发可设 HDHIVE_CHROME 指向 Chrome 可执行文件）")
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
			if e.Response != nil && strings.Contains(e.Response.URL, apiPath) {
				mu.Lock()
				lastRID = e.RequestID
				mu.Unlock()
			}
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
		if err := chromedp.Run(ctx, network.Enable()); err != nil {
			return fmt.Errorf("network.Enable: %w", err)
		}
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
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			select {
			case rid := <-finished:
				if body := captureBody(rid); body != "" {
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
		}
		if err := chromedp.Run(ctx, chromedp.OuterHTML("html", &html)); err != nil {
			return fmt.Errorf("读取渲染结果: %w", err)
		}
		return nil
	}()
	if runErr != nil {
		return "", "", runErr
	}
	mu.Lock()
	defer mu.Unlock()
	return apiHit, html, nil
}
