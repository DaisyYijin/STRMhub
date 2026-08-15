package api

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

	"strmhub/internal/config"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 302 代理服务 ====================

// downloadLinkCache 下载链接缓存 {pickcode -> {url, expiry}}
var downloadLinkCache = make(map[string]downloadCacheEntry)
var downloadCacheMu = sync.Mutex{}

type downloadCacheEntry struct {
	URL    string
	Expiry time.Time
}

// StartProxy 启动302代理服务（独立端口）
func StartProxy(db *gorm.DB, cfg *config.Config) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 定期清理过期下载链接缓存（每 10 分钟），防止内存只增不减
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cleanExpiredCache()
		}
	}()

	// 健康检查
	r.GET("/proxy/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "running", "port": cfg.ProxyPort})
	})

	// 302 代理核心路由: /d/{pickcode} 或 /d/{pickcode}/{filename}
	r.GET("/d/:pickcode", func(c *gin.Context) {
		handleProxyRedirect(c, db, cfg)
	})
	r.GET("/d/:pickcode/*filename", func(c *gin.Context) {
		handleProxyRedirect(c, db, cfg)
	})

	log.Printf("302代理服务启动: http://localhost:%d", cfg.ProxyPort)
	if err := r.Run(":" + cfg.ProxyPortStr()); err != nil {
		log.Printf("代理服务启动失败: %v", err)
	}
}

// handleProxyRedirect 处理 302 重定向请求
func handleProxyRedirect(c *gin.Context, db *gorm.DB, cfg *config.Config) {
	pickcode := c.Param("pickcode")
	if pickcode == "" {
		c.String(http.StatusBadRequest, "missing pickcode")
		return
	}

	log.Printf("302代理请求: pickcode=%s, UA=%s", pickcode, c.Request.UserAgent())

	// 检查缓存
	downloadCacheMu.Lock()
	cached, ok := downloadLinkCache[pickcode]
	downloadCacheMu.Unlock()
	if ok && time.Now().Before(cached.Expiry) {
		log.Printf("302代理命中缓存: %s", pickcode)
		c.Redirect(http.StatusFound, cached.URL)
		return
	}

	// 获取下载链接：OpenAPI 优先，Cookie 回退
	downloadURL, err := proxyDownloadURL(db, cfg, pickcode)
	if err != nil {
		log.Printf("302代理获取下载链接失败: %v", err)
		c.String(http.StatusBadGateway, "获取下载链接失败: %v", err)
		return
	}

	if downloadURL == "" {
		c.String(http.StatusNotFound, "无法获取下载链接")
		return
	}

	// 缓存链接（10 分钟有效）
	downloadCacheMu.Lock()
	downloadLinkCache[pickcode] = downloadCacheEntry{
		URL:    downloadURL,
		Expiry: time.Now().Add(10 * time.Minute),
	}
	downloadCacheMu.Unlock()

	log.Printf("302代理重定向: %s -> %s", pickcode, downloadURL[:min(80, len(downloadURL))]+"...")
	c.Redirect(http.StatusFound, downloadURL)
}

// get115DownloadURL 通过 pickcode 获取 115 文件下载链接
// 首选开放平台 downurl 接口（Cookie 亦可用，p115client download_url_info 同款），
// 旧 webapi files/download 在部分 Cookie 类型下只返回文件元数据不含链接
func get115DownloadURL(pickcode, cookie string) (string, error) {
	// ---- 首选：POST https://proapi.115.com/open/ufile/downurl ----
	form := url.Values{"pick_code": {pickcode}}
	for _, ua := range []string{ua115Unified(), ""} { // 统一 UA 失败再试空 UA（p115client 默认空 UA）
		body, err := post115Form("https://proapi.115.com/open/ufile/downurl", form, cookie, ua, 15*time.Second)
		if err != nil {
			continue
		}
		var env struct {
			State    json.RawMessage          `json:"state"`
			Code     int64                    `json:"code"`
			Message  string                   `json:"message"`
			Data     map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &env) == nil && openStateOK(env.State) && env.Code == 0 {
			for _, v := range env.Data {
				if u := openParseDownloadURL(v); u != "" {
					return u, nil
				}
			}
		}
	}
	// ---- 回退：GET https://webapi.115.com/files/download?pickcode=xxx ----
	apiURL := "https://webapi.115.com/files/download"
	params := fmt.Sprintf("pickcode=%s", pickcode)
	fullURL := apiURL + "?" + params

	body, err := httpGet115(fullURL, nil, cookie, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("请求 115 下载接口失败: %w", err)
	}

	var result struct {
		State bool   `json:"state"`
		URL   struct {
			URL string `json:"url"`
		} `json:"url"`
		// 有些版本返回的格式不同
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		if !result.State {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			msg := e.Error
			if msg == "" {
				msg = "未知错误"
			}
			return "", fmt.Errorf("115 下载接口拒绝: %s", msg)
		}
		if result.URL.URL != "" {
			return result.URL.URL, nil
		}
		if result.Data.URL != "" {
			return result.Data.URL, nil
		}
	}
	// 常规字段为空时用正则兜底提取任意位置的下载链接
	re := regexp.MustCompile(`"url"\s*:\s*"(https?://[^"]+)"`)
	if m := re.FindSubmatch(body); m != nil {
		return string(m[1]), nil
	}
	return "", fmt.Errorf("115 下载响应中未找到链接: %s", truncateStr(string(body), 200))
}

// post115Form 带 Cookie 的 115 表单 POST（可指定 UA，空串表示发空 UA 头）
func post115Form(api string, form url.Values, cookie, ua string, timeout time.Duration) ([]byte, error) {
	throttle115(api)
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return b, nil
}

// 清理过期缓存（定期调用）
func cleanExpiredCache() {
	downloadCacheMu.Lock()
	defer downloadCacheMu.Unlock()
	now := time.Now()
	for k, v := range downloadLinkCache {
		if now.After(v.Expiry) {
			delete(downloadLinkCache, k)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = strings.TrimSpace
