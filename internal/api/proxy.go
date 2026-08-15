package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
func get115DownloadURL(pickcode, cookie string) (string, error) {
	// 115 下载接口: https://webapi.115.com/files/download?pickcode=xxx
	apiURL := "https://webapi.115.com/files/download"
	params := fmt.Sprintf("pickcode=%s", pickcode)
	fullURL := apiURL + "?" + params

	body, err := httpGet115(fullURL, nil, cookie, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("请求 115 下载接口失败: %w", err)
	}

	var result struct {
		State      bool   `json:"state"`
		URL        struct {
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
