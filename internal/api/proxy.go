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
	"strmhub/internal/model"

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
		for {
			select {
			case <-ticker.C:
				cleanExpiredCache()
			case <-stopCh:
				return
			}
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

	// Emby 反代：客户端访问 http://ip:6086/emby 即可使用 Emby（CMS 9096 同款）
	registerEmbyProxy(r, db, cfg)

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
	// 兼容 /d/{pickcode}.{ext}?/{name} 形态：pickcode 段可能带文件后缀，
	// 115 pickcode 为纯字母数字，剥掉最后一个 "." 之后的部分即可
	if i := strings.LastIndex(pickcode, "."); i > 0 {
		pickcode = pickcode[:i]
	}
	// 旧版 STRM 用数字 fid 生成 /d/{fid}/...，查台账换回 pick_code
	if isAllDigits(pickcode) {
		var sf model.SyncedFile
		if err := db.Where("file_id = ?", pickcode).First(&sf).Error; err == nil && sf.PickCode != "" {
			pickcode = sf.PickCode
		}
	}

	reqUA := c.Request.UserAgent()
	log.Printf("302代理请求: pickcode=%s, UA=%s", pickcode, reqUA)

	// 缓存键含 UA：115 直链与签发 UA 绑定，Emby(Lavf) 与浏览器链不可混用
	cacheKey := pickcode + "|" + reqUA
	downloadCacheMu.Lock()
	cached, ok := downloadLinkCache[cacheKey]
	downloadCacheMu.Unlock()
	if ok && time.Now().Before(cached.Expiry) {
		c.Redirect(http.StatusFound, cached.URL)
		return
	}

	// 获取下载链接：按请求方 UA 签发（OpenAPI 优先，Cookie 回退）
	downloadURL, err := proxyDownloadURL(db, cfg, pickcode, reqUA)
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
	downloadLinkCache[cacheKey] = downloadCacheEntry{
		URL:    downloadURL,
		Expiry: time.Now().Add(10 * time.Minute),
	}
	downloadCacheMu.Unlock()

	log.Printf("302代理重定向: %s -> %s", pickcode, downloadURL[:min(80, len(downloadURL))]+"...")
	c.Redirect(http.StatusFound, downloadURL)
}

// ua115Download 下载链路专用 UA（openStrm defaultUA 同款，浏览器 UA 签发的直链
// 在 CDN 侧校验更宽松；直链绑定时也要求下载携带同一 UA）
const ua115Download = "Mozilla/5.0 (iPhone; CPU iPhone OS 15_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/116.0.5845.89 Mobile/15E148 Safari/604.1"

// get115DownloadURL 通过 pickcode 获取 115 文件下载链接
// 首选 App 加密接口（pro.api/android/2.0/ufile/download，openStrm 同款），
// webapi files/download 在部分 Cookie 类型下只返回元数据不含链接。
// 返回的 headers 为 CDN 要求携带的请求头（下载 UA + 直链响应 Set-Cookie）
func get115DownloadURL(pickcode, cookie, signUA string) (string, map[string]string, error) {
	if signUA == "" {
		signUA = ua115Download // 默认浏览器 UA（附属文件下载等自有场景）
	}
	// ---- 首选：App 加密接口（用签发 UA 请求，直链即绑定该 UA）----
	appErr := "未尝试"
	payload, _ := json.Marshal(map[string]string{"pick_code": pickcode})
	form := url.Values{"data": {encrypt115(payload)}}
	body, resp, err := post115FormResp("http://pro.api.115.com/android/2.0/ufile/download", form, cookie, signUA, 15*time.Second)
	if err != nil {
		appErr = "请求失败: " + err.Error()
	} else {
		var env struct {
			State   json.RawMessage `json:"state"`
			ErrNo   int             `json:"errno"`
			ErrCode int             `json:"errcode"`
			Error   string          `json:"error"`
			Data    string          `json:"data"`
		}
		if jerr := json.Unmarshal(body, &env); jerr != nil {
			appErr = "响应非 JSON: " + truncateStr(string(body), 150)
		} else if !openStateOK(env.State) {
			appErr = fmt.Sprintf("state=%s error=%s", strings.TrimSpace(string(env.State)), env.Error)
		} else if env.Data == "" {
			appErr = "响应无加密数据: " + truncateStr(string(body), 150)
		} else {
			plain, derr := decrypt115(env.Data)
			if derr != nil {
				appErr = "解密失败: " + derr.Error()
			} else {
				var d struct {
					URL json.RawMessage `json:"url"`
				}
				var u string
				if json.Unmarshal(plain, &d) == nil {
					u = openParseDownloadURL(d.URL)
				}
				if u == "" {
					if m := regexp.MustCompile(`"url"\s*:\s*"(https?://[^"]+)"`).FindSubmatch(plain); m != nil {
						u = string(m[1])
					}
				}
				if u != "" {
					// 收集直链响应下发的 Set-Cookie（CDN f=3 场景要求回带）
					var parts []string
					for _, ck := range resp.Cookies() {
						parts = append(parts, ck.Name+"="+ck.Value)
					}
					headers := map[string]string{"User-Agent": signUA}
					if len(parts) > 0 {
						headers["Cookie"] = strings.Join(parts, "; ")
					}
					return u, headers, nil
				}
				appErr = "解密后无链接: " + truncateStr(string(plain), 150)
			}
		}
	}

	// ---- 回退：GET https://webapi.115.com/files/download?pickcode=xxx ----
	apiURL := "https://webapi.115.com/files/download"
	params := fmt.Sprintf("pickcode=%s", pickcode)
	fullURL := apiURL + "?" + params

	body, err = httpGet115(fullURL, nil, cookie, 15*time.Second)
	if err != nil {
		return "", nil, fmt.Errorf("获取下载链接失败 [app接口]: %s；[webapi接口]: %v", appErr, err)
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
			return "", nil, fmt.Errorf("获取下载链接失败 [app接口]: %s；[webapi接口]: 拒绝: %s", appErr, msg)
		}
		if result.URL.URL != "" {
			return result.URL.URL, nil, nil
		}
		if result.Data.URL != "" {
			return result.Data.URL, nil, nil
		}
	}
	// 常规字段为空时用正则兜底提取任意位置的下载链接
	re := regexp.MustCompile(`"url"\s*:\s*"(https?://[^"]+)"`)
	if m := re.FindSubmatch(body); m != nil {
		return string(m[1]), nil, nil
	}
	return "", nil, fmt.Errorf("获取下载链接失败 [app接口]: %s；[webapi接口]: %s", appErr, truncateStr(string(body), 150))
}

// post115Form 带 Cookie 的 115 表单 POST（可指定 UA，空串表示发空 UA 头）
func post115Form(api string, form url.Values, cookie, ua string, timeout time.Duration) ([]byte, error) {
	body, _, err := post115FormResp(api, form, cookie, ua, timeout)
	return body, err
}

// post115FormResp 同 post115Form 但额外返回响应对象（读取 Set-Cookie 用）
func post115FormResp(api string, form url.Values, cookie, ua string, timeout time.Duration) ([]byte, *http.Response, error) {
	throttle115(api)
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	throttle115Done(api) // 节流锚点推进到本请求完成时刻
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return b, resp, nil
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

