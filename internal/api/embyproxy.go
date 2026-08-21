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
// 读取顺序：yaml 配置优先，DB settings 表回退
func getEmbyTarget(db *gorm.DB) string {
	embyTargetMu.RLock()
	if time.Since(embyTargetAt) < 5*time.Minute && embyTargetURL != "" {
		v := embyTargetURL
		embyTargetMu.RUnlock()
		return v
	}
	embyTargetMu.RUnlock()

	target := ""
	var cfg struct {
		ServerURL string `json:"server_url"`
	}

	// 先从 yaml 读（前端 saveConfig 写这里）
	// SettingMap 需要通过 config 加载，这里直接走 DB + yaml 两条路
	// yaml 路径：通过全局 config 实例不方便拿，改为同时查 DB 并由调用方传 yaml 值
	// 简化：直接读 DB，同时检查 yaml（通过 Handler 不在这里做）
	if embyConfigYAML != "" {
		if parseJSON(embyConfigYAML, &cfg) == nil && cfg.ServerURL != "" {
			target = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
		}
	}

	// DB 回退
	if target == "" {
		var s struct{ Value string }
		if err := db.Raw("SELECT value FROM settings WHERE `key` = 'emby' LIMIT 1").Scan(&s).Error; err == nil && s.Value != "" {
			if parseJSON(s.Value, &cfg) == nil && cfg.ServerURL != "" {
				target = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
			}
		}
	}

	embyTargetMu.Lock()
	embyTargetURL = target
	embyTargetAt = time.Now()
	embyTargetMu.Unlock()
	return target
}

// embyConfigYAML 从 yaml 配置读取的 emby 配置（由 UpdateEmbyConfig 更新）
var embyConfigYAML string

// UpdateEmbyConfig 外部调用：更新 yaml 配置缓存（保存 emby 配置时触发）
func UpdateEmbyConfig(jsonStr string) {
	embyConfigYAML = jsonStr
	embyTargetMu.Lock()
	embyTargetURL = "" // 清缓存，下次重新读
	embyTargetAt = time.Time{}
	embyTargetMu.Unlock()
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

	// 6086 根路径：非媒体/非API请求 → 反代到 Emby（CMS 9096 同款行为）
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// /d/ 开头是 302 服务已处理；/emby/ 已注册；其余全部反代 Emby
		if strings.HasPrefix(p, "/d/") || strings.HasPrefix(p, "/emby") || strings.HasPrefix(p, "/proxy/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// 反代到 Emby
		target := getEmbyTarget(db)
		if target == "" {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>StrmHub</title></head><body style="font-family:system-ui;max-width:640px;margin:60px auto;padding:0 20px"><h2>StrmHub 302 代理</h2><p style="color:#e74c3c">未配置 Emby 服务器地址，暂无法反代</p><p>请在「系统配置 → EMBY管理」填写 Emby 服务器地址（如 http://192.168.1.100:8096）后刷新本页。</p><hr style="border:none;border-top:1px solid #eee"><p>本端口提供两个服务：</p><ul><li><b>Emby 反代</b>：<code>http://本机IP:6086/</code> 与 <code>/emby</code> — 配置后直接打开即 Emby</li><li><b>302 直连</b>：<code>http://本机IP:6086/d/文件ID</code> — strm 播放地址（自动生成）</li></ul><p>管理后台在 <code>http://本机IP:6060</code></p></body></html>`)
			return
		}
		targetURL, _ := url.Parse(target)
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.Host = targetURL.Host
				req.URL.Scheme = targetURL.Scheme
				req.URL.Host = targetURL.Host
			},
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("[Emby反代] 转发失败: %v", err)
				http.Error(w, "Emby 服务器无法连接: "+err.Error(), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	})
}
