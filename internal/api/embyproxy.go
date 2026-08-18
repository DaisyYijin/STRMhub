package api

// ==================== Emby 反向代理（CMS 9096 同款） ====================
//
// 监听在 302 代理端口（6086）上的 /emby 路径，把请求转发给真正的 Emby 服务器。
// 客户端只需访问 http://NAS_IP:6086/emby/... 即可使用 Emby 全部功能，
// 播放 strm 时的 302 跳转由服务器侧完成——客户端不需要能访问 strm 直连地址。
//
// 用法：
//   Emby 客户端里填的服务器地址 = http://NAS_IP:6086/emby
//   （而不是 http://NAS_IP:8096）
//
// 需要「系统配置 → EMBY管理」里填写 Emby 服务器地址（如 http://192.168.1.100:8096）

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"


	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	embyTargetMu   sync.RWMutex
	embyTargetURL  string
	embyTargetAt   time.Time
)

// getEmbyTarget 获取 Emby 服务器地址（从 EMBY管理 配置读取，5 分钟缓存）
func getEmbyTarget(db *gorm.DB) string {
	embyTargetMu.RLock()
	if time.Since(embyTargetAt) < 5*time.Minute && embyTargetURL != "" {
		v := embyTargetURL
		embyTargetMu.RUnlock()
		return v
	}
	embyTargetMu.RUnlock()

	// 从 DB 读 emby 配置
	target := ""
	var s struct{ Value string }
	if err := db.Raw("SELECT value FROM settings WHERE `key` = 'emby' LIMIT 1").Scan(&s).Error; err == nil && s.Value != "" {
		var cfg struct {
			ServerURL string `json:"server_url"`
		}
		if parseJSON(s.Value, &cfg) == nil {
			target = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
		}
	}

	embyTargetMu.Lock()
	embyTargetURL = target
	embyTargetAt = time.Now()
	embyTargetMu.Unlock()
	return target
}

func parseJSON(data string, out interface{}) error {
	return json.Unmarshal([]byte(data), out)
}

// registerEmbyProxy 在 gin 引擎上注册 Emby 反代路由
func registerEmbyProxy(r *gin.Engine, db *gorm.DB) {
	r.Any("/emby/*path", func(c *gin.Context) {
		target := getEmbyTarget(db)
		if target == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "未配置 Emby 服务器地址，请在「系统配置 → EMBY管理」填写"})
			return
		}

		targetURL, err := url.Parse(target)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Emby 服务器地址无效: " + err.Error()})
			return
		}

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.Host = targetURL.Host
				req.URL.Scheme = targetURL.Scheme
				req.URL.Host = targetURL.Host
				// 保留 /emby 前缀之后完整路径（Emby 的 API 路径本身以 /emby 开头的直接透传，
				// 客户端访问 /emby/Users/... → 转发到 Emby 的 /emby/Users/...）
				req.URL.Path = c.Param("path")
				// 确保路径以 / 开头
				if !strings.HasPrefix(req.URL.Path, "/") {
					req.URL.Path = "/" + req.URL.Path
				}
			},
			FlushInterval: -1, // 流式响应立即刷新（视频播放需要）
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("[Emby反代] 转发失败: %v", err)
				http.Error(w, "Emby 服务器无法连接: "+err.Error(), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	})

	// 根路径友好页面（说明这个端口的用途）
	r.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>StrmHub 302 代理</title>
<style>body{font-family:system-ui;max-width:640px;margin:60px auto;padding:0 20px;color:#333}code{background:#f0f0f0;padding:2px 6px;border-radius:3px}</style>
</head><body>
<h2>StrmHub 302 代理运行中</h2>
<p>本端口提供两个服务：</p>
<ul>
<li><b>Emby 反代</b>：<code>http://本机IP:6086/emby</code> — Emby 客户端填这个地址</li>
<li><b>302 直连</b>：<code>http://本机IP:6086/d/文件ID</code> — strm 播放地址（自动生成）</li>
</ul>
<p>管理后台在 <a href=":6060">http://本机IP:6060</a></p>
</body></html>`)
	})
}
