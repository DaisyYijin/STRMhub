package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"strmhub/internal/config"
	"strmhub/internal/model"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

// buildVersion 构建版本号（main 注入，侧边栏/日志确认运行版本）
var buildVersion = "dev"

// loginGuardEntry 登录防爆破计数
type loginGuardEntry struct {
	fails     int
	lockUntil time.Time
}

var (
	loginGuard   = map[string]loginGuardEntry{}
	loginGuardMu sync.Mutex
)

// SetVersion 注入构建版本号
func SetVersion(v string) { buildVersion = v }

func SetupRoutes(r *gin.RouterGroup, db *gorm.DB, cfg *config.Config) {
	h := &Handler{DB: db, Config: cfg}
	cfgGlobal = cfg

	// 应用用户设置的 115 API 请求间隔（数据库 > 环境变量 > 默认 1s）
	Apply115Interval(db)

	// 启动增量同步 cron 调度器（每分钟检查 incr 配置）
	StartIncrScheduler(h)

	// 启动转存目录守望者（下载完成后 ~1 分钟自动整理）
	StartTransferWatcher(h)

	// 启动离线任务监视器（完成即触发整理；失败告警——磁力不是百分百成功）
	StartOfflineTaskMonitor(h)

	// 启动监控上传引擎（Emby 生成图片回传 115）
	StartMonitorUploader(h)

	// 认证
	auth := r.Group("/auth")
	{
		auth.GET("/status", h.AuthStatus)     // 检查是否已初始化
		auth.POST("/register", h.Register)     // 首次注册
		auth.POST("/login", h.Login)           // 登录
	}

	// 以下接口需要认证
	protected := r.Group("/")
	protected.Use(h.AuthMiddleware())
	{
		// 仪表盘
		protected.GET("/dashboard", h.DashboardEnhanced)

		// 代理测试
		protected.POST("/proxy/test", h.TestProxyLatency)

		// 存储管理
		protected.GET("/storage", h.ListStorage)
		protected.POST("/storage", h.CreateStorage)
		protected.DELETE("/storage/:id", h.DeleteStorage)
		protected.POST("/storage/check", h.CheckStorage)

		// 115 扫码登录
		protected.POST("/storage/qrcode", h.CreateQrCode)
		protected.POST("/storage/qrcode/status", h.QrCodeStatus)

		// 115 开放平台扫码授权（OpenAPI）
		protected.POST("/storage/open/qrcode", h.CreateOpenQrCode)
		protected.POST("/storage/open/qrcode/status", h.OpenQrCodeStatus)

		// 目录浏览
		protected.GET("/storage/115/dirs", h.List115Dirs)
		protected.GET("/storage/115/resolve", h.Resolve115Path)
		protected.GET("/storage/local/dirs", h.ListLocalDirs)

		// 核心配置（TMDB）
		protected.GET("/config/tmdb", h.GetTmdbConfig)
		protected.POST("/config/tmdb", h.SaveTmdbConfig)
		protected.GET("/config/setting", h.GetSetting)
		protected.POST("/config/setting", h.SaveSetting)
		protected.POST("/config/test-gpt", h.TestGPTConnection)
		protected.POST("/config/test-tmdb", h.TestTMDBConnection)

		// Emby 连接测试
		protected.POST("/config/test-emby", h.TestEmbyConnection)

		// 消息通知
		protected.POST("/message/test", h.TestMessage)

		// 归档同步
		protected.GET("/sync/tasks", h.ListSyncTasks)
		protected.POST("/sync/tasks", h.CreateSyncTask)
		protected.POST("/sync/tasks/:id/run", h.RunSyncTask)
		protected.GET("/sync/tasks/:id/logs", h.GetSyncLogs)
		protected.POST("/sync/full", h.RunFullSync)
		protected.POST("/sync/incremental", h.RunIncrementalSync)

		// Cron 未来运行时间预览（校验表达式是否正确）
		protected.POST("/sync/cron-preview", func(c *gin.Context) {
			var req struct {
				Cron string `json:"cron"`
			}
			if err := c.ShouldBindJSON(&req); err != nil || req.Cron == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 cron 表达式"})
				return
			}
			var next []string
			t := time.Now()
			for i := 0; i < 5; i++ {
				t = nextCronTime(req.Cron, t)
				if t.IsZero() {
					break
				}
				next = append(next, t.Format("01-02 15:04"))
			}
			if len(next) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效或未来一年内不会触发"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"next": next})
		})

		// 任务状态（前端轮询：任务进行中禁用同步/整理按钮）+ 最近运行记录
		protected.GET("/sync/status", func(c *gin.Context) {
			running, name, start := TaskStatus()
			g := gin.H{"running": running}
			if running {
				g["task"] = name
				g["since"] = start.Format("15:04:05")
				g["elapsed"] = time.Since(start).Truncate(time.Second).String()
			}
			g["recent"] = GetRecentRuns()
			c.JSON(http.StatusOK, g)
		})

		// STRM 管理
		// 302 直连（与 6086 代理同款，6060 也能作为 strm 直连地址，CMS 二合一模式）
		r.GET("/d/:pickcode", func(c *gin.Context) { handleProxyRedirect(c, h.DB, h.Config) })
		r.GET("/d/:pickcode/*filename", func(c *gin.Context) { handleProxyRedirect(c, h.DB, h.Config) })

		protected.GET("/strm", h.ListStrmFiles)
		protected.DELETE("/strm/:id", h.DeleteStrmFile)
						
		// 刮削整理
		protected.GET("/scrape/rules", h.ListScrapeRules)
		protected.POST("/scrape/rules", h.SaveScrapeRules)
		protected.GET("/scrape/categories", h.ListCategories)
		protected.POST("/scrape/categories", h.SaveCategories)
		protected.POST("/strm/cleanup", h.OrphanCleanup)

		// 版本号与日志级别
		protected.GET("/version", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"version": buildVersion})
		})
		protected.POST("/system/log-level", func(c *gin.Context) {
			var req struct {
				Level string `json:"level"` // simple / verbose
			}
			if err := c.ShouldBindJSON(&req); err != nil || (req.Level != "simple" && req.Level != "verbose") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
				return
			}
			if err := cfg.SaveSetting("log-level", req.Level); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			// 立即刷新缓存
			logVerboseMu.Lock()
			logVerboseAt = time.Time{}
			logVerboseMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"message": "已保存"})
		})

		protected.GET("/scrape/wash", h.ListWashRules)
		protected.POST("/scrape/wash", h.SaveWashRules)

		// 整理→同步闭环
		protected.POST("/organize/pipeline", h.RunOrganizePipeline)

		// 分享链接转存（转存到接收文件夹后由整理+增量接管）
		protected.POST("/share/receive", h.ShareReceive)

		// 离线下载（磁力/ed2k/HTTP）
		protected.POST("/offline/add", h.offlineAddTask)
		protected.GET("/offline/tasks", h.offlineTaskList)

		// 重置整理记录（清空 MediaLibrary 去重表，误判已存在时使用）
		protected.POST("/organize/reset-records", func(c *gin.Context) {
			var rec model.MediaLibrary
			total := h.DB.Model(&rec).Where("1 = 1").Delete(&rec).RowsAffected
			log.Printf("[整理] 已重置整理记录: 清除 %d 条", total)
			c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清除 %d 条整理记录", total), "cleared": total})
		})

		// 302 代理
		protected.GET("/proxy/status", h.ProxyStatus)
		protected.POST("/proxy/config", h.SaveProxyConfig)

		// 系统设置
		protected.GET("/settings", h.GetSettings)
		protected.POST("/settings", h.SaveSettings)
		protected.GET("/system/logs", h.GetSystemLogs)
		protected.GET("/storage/115/diagnose", h.Diagnose115)
	}
}

type Handler struct {
	DB     *gorm.DB
	Config *config.Config
}

// ==================== 认证 ====================

func (h *Handler) AuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"initialized": h.Config.IsAuthExists(),
	})
}

func (h *Handler) Register(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if h.Config.IsAuthExists() {
		c.JSON(http.StatusForbidden, gin.H{"error": "系统已初始化，不允许注册"})
		return
	}

	if err := h.Config.SaveAuth(req.Username, req.Password); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "注册失败: " + err.Error()})
		return
	}

	token := h.generateToken(1, req.Username)
	c.JSON(http.StatusOK, gin.H{
		"token":    token,
		"username": req.Username,
		"message":  "注册成功",
	})
}

func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 防爆破：同 IP 连续 5 次失败锁定 10 分钟
	ip := c.ClientIP()
	loginGuardMu.Lock()
	g, ok := loginGuard[ip]
	if ok && time.Now().Before(g.lockUntil) {
		remain := time.Until(g.lockUntil).Truncate(time.Second)
		loginGuardMu.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("失败次数过多，已锁定，请 %s 后再试", remain)})
		return
	}
	loginGuardMu.Unlock()
	if !h.Config.VerifyAuth(req.Username, req.Password) {
		loginGuardMu.Lock()
		g := loginGuard[ip]
		g.fails++
		if g.fails >= 5 {
			g.lockUntil = time.Now().Add(10 * time.Minute)
			g.fails = 0
		}
		loginGuard[ip] = g
		loginGuardMu.Unlock()
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}
	loginGuardMu.Lock()
	delete(loginGuard, ip)
	loginGuardMu.Unlock()

	token := h.generateToken(1, req.Username)
	c.JSON(http.StatusOK, gin.H{
		"token":    token,
		"username": req.Username,
		"message":  "登录成功",
	})
}

func (h *Handler) generateToken(userID uint, username string) string {
	claims := jwt.MapClaims{
		"user_id":  userID,
		"username": username,
		// 自托管工具常用场景：30 天有效期（此前 72 小时，三天没操作就会被
		// 全站 401 轰炸着赶去重新登录，体验差且无安全收益——密码仍可随时改）
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t, _ := token.SignedString([]byte(h.Config.JWTSecret))
	return t
}

func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}
		// 去掉 Bearer 前缀
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}
		// 验证 JWT
		claims := jwt.MapClaims{}
		_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(h.Config.JWTSecret), nil
		})
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
			c.Abort()
			return
		}
		c.Set("user_id", claims["user_id"])
		c.Set("username", claims["username"])
		c.Next()
	}
}

// ==================== 仪表盘 ====================

func (h *Handler) Dashboard(c *gin.Context) {
	// 统计数据
	var strmCount int64
	h.DB.Model(&model.StrmFile{}).Count(&strmCount)

	var storageCount int64
	h.DB.Model(&model.Storage{}).Count(&storageCount)

	var invalidCount int64
	h.DB.Model(&model.StrmFile{}).Where("status = ?", "invalid").Count(&invalidCount)

	c.JSON(http.StatusOK, gin.H{
		"pan115": h.pan115CapacityCached(),
		"server": gin.H{
			"cpu":    "23%",
			"memory": "1.2G/8G",
			"disk":   "45G/500G",
			"uptime": "7d",
		},
		"emby": gin.H{
			"connected":    false,
			"libraries":    0,
			"media_count":  strmCount,
			"playing":      0,
			"today_added":  0,
		},
		"storage": gin.H{
			"total":  storageCount,
			"online": storageCount,
		},
		"strm": gin.H{
			"total":   strmCount,
			"invalid": invalidCount,
		},
		"proxy": gin.H{
			"enabled":  true,
			"port":     h.Config.ProxyPort,
			"running":  true,
		},
	})
}

// ==================== 存储管理（占位） ====================

func (h *Handler) ListStorage(c *gin.Context) {
	var storages []model.Storage
	h.DB.Find(&storages)
	c.JSON(http.StatusOK, gin.H{"data": storages})
}

func (h *Handler) CreateStorage(c *gin.Context) {
	var storage model.Storage
	if err := c.ShouldBindJSON(&storage); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// upsert：按 type 去重，存在则更新（保留已有 Cookie 和账号名）
	var existing model.Storage
	if err := h.DB.Where("type = ?", storage.Type).First(&existing).Error; err == nil {
		updates := map[string]interface{}{
			"cookie_path":     storage.CookiePath,
			"device":          storage.Device,
			"interval":        storage.Interval,
			"openapi_enabled": storage.OpenapiEnabled,
			"app_id":          storage.AppID,
			"app_key":         storage.AppKey,
		}
		if storage.Name != "" && storage.Name != "115主号" {
			updates["name"] = storage.Name
		}
		if storage.Cookie != "" {
			updates["cookie"] = storage.Cookie
		}
		h.DB.Model(&existing).Updates(updates)
		// 间隔设置变更立即生效
		if storage.Type == "115" {
			Apply115Interval(h.DB)
		}
		c.JSON(http.StatusOK, gin.H{"data": existing, "message": "保存成功"})
		return
	}
	// 不存在则创建
	if storage.Name == "" {
		storage.Name = "115主号"
	}
	h.DB.Create(&storage)
	if storage.Type == "115" {
		Apply115Interval(h.DB)
	}
	c.JSON(http.StatusOK, gin.H{"data": storage, "message": "创建成功"})
}

func (h *Handler) DeleteStorage(c *gin.Context) {
	id := c.Param("id")
	h.DB.Delete(&model.Storage{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

// CheckStorage 检测 115 账号可用性：OpenAPI 优先，Cookie 回退
func (h *Handler) CheckStorage(c *gin.Context) {
	var req struct {
		Type       string `json:"type"`
		CookiePath string `json:"cookie_path"`
		Cookie     string `json:"cookie"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// ===== OpenAPI 通道 =====
	if oc := h.getOpen115(); oc != nil && oc.authorized() {
		if err := oc.ping(); err != nil {
			log.Printf("[系统] OpenAPI 校验失败: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{
				"valid":   false,
				"message": "OpenAPI 校验失败：" + err.Error(),
			})
			return
		}
		var storage model.Storage
		name := "OpenAPI"
		if err := h.DB.Where("type = ?", "115").First(&storage).Error; err == nil && storage.Name != "" {
			name = storage.Name
		}
		c.JSON(http.StatusOK, gin.H{
			"username": name,
			"capacity": "OpenAPI 通道（官方授权）",
			"valid":    true,
			"message":  "OpenAPI 授权有效",
			"channel":  "OpenAPI",
		})
		return
	}

	// ===== Cookie 通道 =====
	// 支持手动导入：请求带 cookie 字段时优先使用，检测通过后自动保存
	cookie := strings.TrimSpace(req.Cookie)
	if cookie == "" {
		cookie, _ = h.get115Cookie()
	}
	if cookie == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未绑定 115 账号"})
		return
	}
	// 基本格式校验，避免保存明显无效的内容
	if !strings.Contains(cookie, "UID=") || !strings.Contains(cookie, "SEID=") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cookie 格式不正确，需要包含 UID=...;CID=...;SEID=... 字段"})
		return
	}

	log.Printf("[系统] 检测 Cookie（长度=%d）", len(cookie))

	// 用 web 端用户信息接口校验 Cookie（my.115.com nav，115driver ApiUserInfo 同款）
	// 注意：不能用 proapi.115.com/android/* 的 App 专用接口，那些接口要求对应 App 的 UA
	const navAPI = "https://my.115.com/?ct=ajax&ac=nav"
	body, err := httpGet115UA(navAPI, nil, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		log.Printf("[系统] 调用失败: %v", err)
		c.JSON(http.StatusOK, gin.H{
			"valid":   false,
			"message": "调用 115 接口失败：" + err.Error(),
		})
		return
	}

	var resp struct {
		State bool            `json:"state"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"valid":   false,
			"message": "解析 115 响应失败",
		})
		return
	}
	// state=false 时 data 可能是 []，仅在 state=true 时解析用户信息
	var userName string
	accInfo := gin.H{}
	if resp.State && len(resp.Data) > 0 {
		var d struct {
			UserName    string `json:"user_name"`
			UserID      int64  `json:"user_id"`
			Face        string `json:"face"`   // 头像 URL
			Vip         int    `json:"vip"`    // 0=非会员 1/2=会员
			Expire      int64  `json:"expire"` // 会员到期时间戳（秒）
			Forever     int    `json:"forever"`
			IsPrivilege bool   `json:"is_privilege"`
		}
		_ = json.Unmarshal(resp.Data, &d)
		userName = d.UserName
		accInfo = gin.H{
			"avatar":        d.Face,
			"user_id":       d.UserID,
			"vip":           d.Vip,
			"vip_expire":    d.Expire,
			"vip_forever":   d.Forever,
			"is_privilege":  d.IsPrivilege,
		}
	}
	if !resp.State || userName == "" {
		msg := resp.Error
		if msg == "" {
			msg = "Cookie 无效或已过期"
		}
		c.JSON(http.StatusOK, gin.H{
			"valid":   false,
			"message": msg + "，请重新扫码登录",
		})
		return
	}

	// 容量信息（webapi files/index_info，115driver GetInfo 同款）
	capacity := "-"
	var usedSize, totalSize int64
	const infoAPI = "https://webapi.115.com/files/index_info"
	if infoBody, err := httpGet115UA(infoAPI, nil, cookie, ua115Unified(), 15*time.Second); err == nil {
		var info struct {
			State bool `json:"state"`
			Data  struct {
				SpaceInfo struct {
					AllTotal struct {
						Size int64  `json:"size"`
						Fmt  string `json:"size_format"`
					} `json:"all_total"`
					AllUse struct {
						Size int64  `json:"size"`
						Fmt  string `json:"size_format"`
					} `json:"all_use"`
				} `json:"space_info"`
			} `json:"data"`
		}
		if json.Unmarshal(infoBody, &info) == nil && info.State && info.Data.SpaceInfo.AllTotal.Size > 0 {
			usedSize = info.Data.SpaceInfo.AllUse.Size
			totalSize = info.Data.SpaceInfo.AllTotal.Size
			capacity = fmt.Sprintf("%s / %s", formatBytes(usedSize), formatBytes(totalSize))
		}
		// 登录设备列表（排查风控/异常登录用）
		var info2 struct {
			State bool `json:"state"`
			Data  struct {
				LoginDevicesInfo struct {
					List []struct {
						Name      string `json:"name"`
						Device    string `json:"device"`
						IP        string `json:"ip"`
						City      string `json:"city"`
						Utime     int64  `json:"utime"`
						IsCurrent int    `json:"is_current"`
					} `json:"list"`
				} `json:"login_devices_info"`
			} `json:"data"`
		}
		if json.Unmarshal(infoBody, &info2) == nil && info2.State && len(info2.Data.LoginDevicesInfo.List) > 0 {
			devices := make([]gin.H, 0, len(info2.Data.LoginDevicesInfo.List))
			for _, d := range info2.Data.LoginDevicesInfo.List {
				devices = append(devices, gin.H{
					"name": d.Name, "device": d.Device, "ip": d.IP,
					"city": d.City, "utime": d.Utime, "is_current": d.IsCurrent == 1,
				})
			}
			accInfo["devices"] = devices
		}
	}
	accInfo["used_size"] = usedSize
	accInfo["total_size"] = totalSize

	resp2 := gin.H{
		"username": userName,
		"capacity": capacity,
		"valid":    true,
		"message":  "Cookie 有效",
		"channel":  "Cookie",
	}
	for k, v := range accInfo {
		resp2[k] = v
	}
	c.JSON(http.StatusOK, resp2)

	// 手动导入的 Cookie 检测通过后自动保存（绕过被风控的扫码登录接口）
	if strings.TrimSpace(req.Cookie) != "" {
		h.Config.SaveCookie(cookie)
		h.Config.Save115Device("web")
		h.upsert115Storage(cookie, "web", userName)
		log.Printf("[系统] 已导入并保存手动提供的 Cookie，账号=%s", userName)
	}
}

// pan115Capacity 115 容量信息（仪表盘用，5 分钟内存缓存）
type pan115Cap struct {
	Username  string `json:"username"`
	Used      int64  `json:"used"`
	Total     int64  `json:"total"`
	FetchedAt int64  `json:"-"`
}

var (
	pan115CapMu    sync.Mutex
	pan115CapCache *pan115Cap
)

// pan115CapacityCached 带缓存读取 115 容量（Cookie 有效才查询）
func (h *Handler) pan115CapacityCached() gin.H {
	pan115CapMu.Lock()
	cached := pan115CapCache
	pan115CapMu.Unlock()
	if cached != nil && time.Since(time.Unix(cached.FetchedAt, 0)) < 5*time.Minute {
		return gin.H{"username": cached.Username, "used": cached.Used, "total": cached.Total,
			"used_h": formatBytes(cached.Used), "total_h": formatBytes(cached.Total)}
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return gin.H{"enabled": false}
	}
	const navAPI = "https://my.115.com/?ct=ajax&ac=nav"
	body, err := httpGet115UA(navAPI, nil, cookie, ua115Unified(), 10*time.Second)
	if err != nil {
		return gin.H{"enabled": false}
	}
	var resp struct {
		State bool            `json:"state"`
		Data  json.RawMessage `json:"data"`
	}
	cap2 := &pan115Cap{}
	if json.Unmarshal(body, &resp) == nil && resp.State {
		var d struct {
			UserName string `json:"user_name"`
		}
		_ = json.Unmarshal(resp.Data, &d)
		cap2.Username = d.UserName
	}
	if infoBody, err := httpGet115UA("https://webapi.115.com/files/index_info", nil, cookie, ua115Unified(), 10*time.Second); err == nil {
		var info struct {
			State bool `json:"state"`
			Data  struct {
				SpaceInfo struct {
					AllTotal struct{ Size int64 } `json:"all_total"`
					AllUse   struct{ Size int64 } `json:"all_use"`
				} `json:"space_info"`
			} `json:"data"`
		}
		if json.Unmarshal(infoBody, &info) == nil && info.State {
			cap2.Used, cap2.Total = info.Data.SpaceInfo.AllUse.Size, info.Data.SpaceInfo.AllTotal.Size
		}
	}
	cap2.FetchedAt = time.Now().Unix()
	pan115CapMu.Lock()
	pan115CapCache = cap2
	pan115CapMu.Unlock()
	return gin.H{"username": cap2.Username, "used": cap2.Used, "total": cap2.Total,
		"used_h": formatBytes(cap2.Used), "total_h": formatBytes(cap2.Total)}
}

// formatBytes 将字节数格式化为人类可读容量（如 1.50 GiB）
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ==================== 核心配置 TMDB ====================

func (h *Handler) GetTmdbConfig(c *gin.Context) {
	var cfg model.TmdbConfig
	result := h.DB.First(&cfg)
	if result.Error != nil {
		// 返回默认值
		c.JSON(http.StatusOK, gin.H{
			"api_key":        "",
			"api_url":        "https://api.themoviedb.org",
			"image_api_url":  "https://image.tmdb.org",
			"language":       "zh-CN",
			"image_language": "zh-CN",
			"enable_proxy":   false,
			"proxy_url":      "",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

func (h *Handler) SaveTmdbConfig(c *gin.Context) {
	var cfg model.TmdbConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if cfg.ID > 0 {
		h.DB.Save(&cfg)
	} else {
		h.DB.Create(&cfg)
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg, "message": "保存成功"})
}

// ==================== 归档同步（占位） ====================

func (h *Handler) ListSyncTasks(c *gin.Context) {
	var tasks []model.SyncTask
	h.DB.Find(&tasks)
	c.JSON(http.StatusOK, gin.H{"data": tasks})
}

func (h *Handler) CreateSyncTask(c *gin.Context) {
	var task model.SyncTask
	if err := c.ShouldBindJSON(&task); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	h.DB.Create(&task)
	c.JSON(http.StatusOK, gin.H{"data": task, "message": "创建成功"})
}

func (h *Handler) RunSyncTask(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "同步任务已启动（开发中）"})
}

func (h *Handler) GetSyncLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": []gin.H{}})
}

// ==================== STRM 管理（占位） ====================

func (h *Handler) ListStrmFiles(c *gin.Context) {
	var files []model.StrmFile
	query := h.DB.Model(&model.StrmFile{})
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	query.Find(&files)
	c.JSON(http.StatusOK, gin.H{"data": files})
}

func (h *Handler) DeleteStrmFile(c *gin.Context) {
	id := c.Param("id")
	h.DB.Delete(&model.StrmFile{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}


// ==================== 刮削整理（占位） ====================

func (h *Handler) ListScrapeRules(c *gin.Context) {
	var rules []model.ScrapeRule
	h.DB.Find(&rules)
	c.JSON(http.StatusOK, gin.H{"data": rules})
}

func (h *Handler) SaveScrapeRules(c *gin.Context) {
	var rules []model.ScrapeRule
	if err := c.ShouldBindJSON(&rules); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	for _, r := range rules {
		if r.ID > 0 {
			h.DB.Save(&r)
		} else {
			h.DB.Create(&r)
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// TestGPTConnection 测试 GPT 连接
// POST /config/test-gpt  body: {"url":"...","key":"...","model":"..."}
func (h *Handler) TestGPTConnection(c *gin.Context) {
	var req struct {
		URL   string `json:"url"`
		Key   string `json:"key"`
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" || req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 API 地址和模型名称"})
		return
	}
	// 向 OpenAI 协议的 /v1/chat/completions 发一个简单的测试请求
	body, _ := json.Marshal(map[string]interface{}{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 5,
	})
	client := &http.Client{Timeout: 15 * time.Second}
	endpoint := strings.TrimRight(req.URL, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "请求构建失败: " + err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Key)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "连接失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "连接成功，模型响应正常"})
}

// TestTMDBConnection 测试 TMDB 连接
// POST /config/test-tmdb  body: {"api_url":"...","api_key":"...","language":"..."}
func (h *Handler) TestTMDBConnection(c *gin.Context) {
	var req struct {
		APIURL   string `json:"api_url"`
		APIKey   string `json:"api_key"`
		Language string `json:"language"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 TMDB API 密钥"})
		return
	}
	req.APIURL = normalizeTMDBBase(req.APIURL)
	if req.Language == "" {
		req.Language = "zh-CN"
	}
	// 用 /configuration 接口测试（/3 前缀已在规范化时补齐）
	endpoint := req.APIURL + "/configuration?api_key=" + req.APIKey
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "连接失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "TMDB 连接成功"})
}

func (h *Handler) ListCategories(c *gin.Context) {
	// 从 ScrapeRule 表读取已保存的 YAML
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "category_config").First(&rule)
	c.JSON(http.StatusOK, gin.H{"config": rule.Config})
}

// SaveCategories 保存二级分类（支持 YAML 字符串，对齐 CMS）
func (h *Handler) SaveCategories(c *gin.Context) {
	var req struct {
		Yaml string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 存储 YAML 到 ScrapeRule 表（type=category_config）
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "category_config").First(&rule)
	rule.Type = "category_config"
	rule.Enabled = true
	rule.Config = req.Yaml
	if rule.ID > 0 {
		h.DB.Save(&rule)
	} else {
		h.DB.Create(&rule)
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ==================== 洗版策略 ====================

func (h *Handler) ListWashRules(c *gin.Context) {
	// 从 ScrapeRule 表读取已保存的 YAML
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "wash_config").First(&rule)
	c.JSON(http.StatusOK, gin.H{"config": rule.Config})
}

// SaveWashRules 保存洗版策略（支持 YAML 字符串，对齐 CMS）
func (h *Handler) SaveWashRules(c *gin.Context) {
	var req struct {
		Yaml string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "wash_config").First(&rule)
	rule.Type = "wash_config"
	rule.Enabled = true
	rule.Config = req.Yaml
	if rule.ID > 0 {
		h.DB.Save(&rule)
	} else {
		h.DB.Create(&rule)
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ==================== 302 代理（占位） ====================

func (h *Handler) ProxyStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled":  true,
		"port":     h.Config.ProxyPort,
		"running":  true,
		"uptime":   "7d",
		"requests": 45231,
		"redirects": 12847,
	})
}

func (h *Handler) SaveProxyConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ==================== 系统设置（占位） ====================

func (h *Handler) GetSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"port":          h.Config.Port,
		"proxy_port":    h.Config.ProxyPort,
		"notifications": gin.H{"telegram": false, "webhook": false},
	})
}

func (h *Handler) SaveSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// GetSystemLogs 读取系统日志文件最后 500 行
// GET /system/logs（日志页唯一数据源：整理/同步/转存等任务动作的实时输出）
func (h *Handler) GetSystemLogs(c *gin.Context) {
	logPath := "/logs/app.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"logs": "暂无日志文件（日志文件在 /logs/app.log）"})
		return
	}
	lines := strings.Split(string(data), "\n")
	// 只返回最后 500 行（一轮整理/同步动辄上百行，200 行看不全一个完整动作）
	start := 0
	if len(lines) > 500 {
		start = len(lines) - 500
	}
	c.JSON(http.StatusOK, gin.H{"logs": strings.Join(lines[start:], "\n")})
}

// Diagnose115 诊断 115 连接问题：会话有效性 / 域名风控 / UA 配对全矩阵探测
// GET /storage/115/diagnose
func (h *Handler) Diagnose115(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	results := gin.H{}
	probe := func(name, api string, query url.Values, ua string) (ok bool, info string) {
		body, err := httpGet115Full(api, query, cookie, ua, 15*time.Second, nil)
		if err != nil {
			return false, err.Error()
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Count int    `json:"count"`
			Data  []struct {
				N string `json:"n"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return false, "非 JSON 响应: " + truncateStr(string(body), 100)
		}
		if !r.State {
			return false, r.Error
		}
		return true, fmt.Sprintf("成功，count=%d，示例: %s", r.Count, firstDiagName(r.Data))
	}

	// 1. 会话有效性（my.115.com 不做设备校验）
	results["会话检测(my.115.com)"] = diagResult(probe("nav", "https://my.115.com/?ct=ajax&ac=nav", nil, ua115Unified()))

	// 2. 无参数探测（p115client：被风控的 /files 不带参数仍可用，可判定域名是否被标记）
	results["无参数探测(webapi.115.com)"] = diagResult(probe("noparam", "https://webapi.115.com/files", nil, ua115Unified()))

	// 3. 各镜像域名（统一 UA + 标准参数）
	for _, origin := range webapiFileOrigins {
		name := "列目录(" + strings.TrimPrefix(origin, "https://") + ")"
		results[name] = diagResult(probe("files", origin+"/files", build115FileQuery("0", 0), ua115Unified()))
	}

	// 4. 各 UA（主域名 + 标准参数）
	uaList := []struct{ name, ua string }{
		{"统一UA(115Browser)", ua115Unified()},
		{"朴素UA(Mozilla/5.0)", "Mozilla/5.0"},
		{"Chrome浏览器UA", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"},
		{"115disk UA", "Mozilla/5.0 115disk/30.1.0"},
	}
	for _, u := range uaList {
		results["UA测试-"+u.name] = diagResult(probe("files", "https://webapi.115.com/files", build115FileQuery("0", 0), u.ua))
	}

	c.JSON(http.StatusOK, gin.H{
		"cookie_len": len(cookie),
		"results":    results,
		"hint":       "判读：会话检测失败=Cookie 无效需重新扫码；无参数探测成功但列目录失败=域名被风控（镜像行若有成功项则已自动回退可用）；所有列目录都失败但会话成功=UA 配对或账号风控问题，把本报告发回。",
	})
}

// diagResult 包装探测结果
func diagResult(ok bool, info string) gin.H {
	return gin.H{"ok": ok, "info": info}
}

// firstDiagName 取第一个文件名（无数据返回空）
func firstDiagName(data []struct {
	N string `json:"n"`
}) string {
	if len(data) == 0 {
		return "-"
	}
	return data[0].N
}

// GetSetting 获取通用配置
// GET /config/setting?key=strm
func (h *Handler) GetSetting(c *gin.Context) {
	key := c.Query("key")
	value := h.Config.GetSetting(key)
	// 回退到数据库（兼容旧数据）
	if value == "" {
		var s model.Setting
		if err := h.DB.Where("key = ?", key).First(&s).Error; err == nil {
			value = s.Value
		}
	}
	c.JSON(http.StatusOK, gin.H{"key": key, "value": value})
}

// SaveSetting 保存通用配置（key-value，value 为 JSON 字符串）
// POST /config/setting  body: {"key":"strm","value":"{...}"}
func (h *Handler) SaveSetting(c *gin.Context) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := h.Config.SaveSetting(req.Key, req.Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	// emby 配置保存时同步更新反代缓存
	if req.Key == "emby" {
		UpdateEmbyConfig(req.Value)
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}
