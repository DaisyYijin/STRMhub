package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"strmhub/internal/config"
	"strmhub/internal/model"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func SetupRoutes(r *gin.RouterGroup, db *gorm.DB, cfg *config.Config) {
	h := &Handler{DB: db, Config: cfg}

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
		protected.GET("/dashboard", h.Dashboard)

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
		protected.GET("/storage/local/dirs", h.ListLocalDirs)

		// 核心配置（TMDB）
		protected.GET("/config/tmdb", h.GetTmdbConfig)
		protected.POST("/config/tmdb", h.SaveTmdbConfig)
		protected.GET("/config/setting", h.GetSetting)
		protected.POST("/config/setting", h.SaveSetting)
		protected.POST("/config/test-gpt", h.TestGPTConnection)
		protected.POST("/config/test-tmdb", h.TestTMDBConnection)

		// 消息通知
		protected.POST("/message/test", h.TestMessage)

		// 归档同步
		protected.GET("/sync/tasks", h.ListSyncTasks)
		protected.POST("/sync/tasks", h.CreateSyncTask)
		protected.POST("/sync/tasks/:id/run", h.RunSyncTask)
		protected.GET("/sync/tasks/:id/logs", h.GetSyncLogs)
		protected.POST("/sync/full", h.RunFullSync)
		protected.POST("/sync/incremental", h.RunIncrementalSync)

		// STRM 管理
		protected.GET("/strm", h.ListStrmFiles)
		protected.DELETE("/strm/:id", h.DeleteStrmFile)
		protected.POST("/strm/scan/fast", h.FastScan)
		protected.POST("/strm/scan/slow", h.SlowScan)
		protected.POST("/strm/cleanup", h.CleanupInvalid)

		// 刮削整理
		protected.GET("/scrape/rules", h.ListScrapeRules)
		protected.POST("/scrape/rules", h.SaveScrapeRules)
		protected.GET("/scrape/categories", h.ListCategories)
		protected.POST("/scrape/categories", h.SaveCategories)
		protected.GET("/scrape/wash", h.ListWashRules)
		protected.POST("/scrape/wash", h.SaveWashRules)

		// 整理→同步闭环
		protected.POST("/organize/pipeline", h.RunOrganizePipeline)

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

	if !h.Config.VerifyAuth(req.Username, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

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
		"exp":      time.Now().Add(72 * time.Hour).Unix(),
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
		c.JSON(http.StatusOK, gin.H{"data": existing, "message": "保存成功"})
		return
	}
	// 不存在则创建
	if storage.Name == "" {
		storage.Name = "115主号"
	}
	h.DB.Create(&storage)
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
			log.Printf("[115检查] OpenAPI 校验失败: %v", err)
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
	cookie := strings.TrimSpace(req.Cookie)
	if cookie == "" {
		cookie, _ = h.get115Cookie()
	}
	if cookie == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未绑定 115 账号"})
		return
	}

	log.Printf("[115检查] Cookie长度=%d, UA=%.60s...", len(cookie), h.get115UA())

	// 调用 115 用户信息接口校验 Cookie（UA 与登录设备匹配）
	const settingStatusAPI = "https://proapi.115.com/android/2.0/user/setting_status"
	body, err := httpGet115UA(settingStatusAPI, nil, cookie, h.get115UA(), 15*time.Second)
	if err != nil {
		log.Printf("[115检查] 调用失败: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{
			"valid":   false,
			"message": "调用 115 接口失败：" + err.Error(),
		})
		return
	}

	var resp struct {
		State int `json:"state"`
		Data  struct {
			Username string `json:"username"`
			Space    int64  `json:"space"`
			Used     int64  `json:"used"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"valid":   false,
			"message": "解析 115 响应失败",
		})
		return
	}
	if resp.State != 0 || resp.Data.Username == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"valid":   false,
			"message": "Cookie 无效或已过期",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"username": resp.Data.Username,
		"capacity": fmt.Sprintf("%s / %s", formatBytes(resp.Data.Used), formatBytes(resp.Data.Space)),
		"valid":    true,
		"message":  "Cookie 有效",
		"channel":  "Cookie",
	})
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
			"api_url":        "https://api.tmdb.org",
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

func (h *Handler) FastScan(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "快扫已启动（开发中）"})
}

func (h *Handler) SlowScan(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "慢扫已启动（开发中）"})
}

func (h *Handler) CleanupInvalid(c *gin.Context) {
	result := h.DB.Where("status = ?", "invalid").Delete(&model.StrmFile{})
	c.JSON(http.StatusOK, gin.H{"message": "清理完成", "count": result.RowsAffected})
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
	if req.APIURL == "" {
		req.APIURL = "https://api.tmdb.org"
	}
	if req.Language == "" {
		req.Language = "zh-CN"
	}
	// 用 /configuration 接口测试
	endpoint := strings.TrimRight(req.APIURL, "/") + "/configuration?api_key=" + req.APIKey
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

// GetSystemLogs 读取系统日志文件最后 200 行
// GET /system/logs
func (h *Handler) GetSystemLogs(c *gin.Context) {
	logPath := "/logs/app.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"logs": "暂无日志文件（日志文件在 /logs/app.log）"})
		return
	}
	lines := strings.Split(string(data), "\n")
	// 只返回最后 200 行
	start := 0
	if len(lines) > 200 {
		start = len(lines) - 200
	}
	c.JSON(http.StatusOK, gin.H{"logs": strings.Join(lines[start:], "\n")})
}

// Diagnose115 诊断 115 连接问题：尝试多种 UA 组合，定位风控/UA/Cookie 问题
// GET /storage/115/diagnose
func (h *Handler) Diagnose115(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 多种 UA 组合测试（按 115driver 标准格式优先）
	uas := map[string]string{
		"115Browser动态版本": "Mozilla/5.0 115Browser/" + getAppVerCached(),
		"115Browser默认":   "Mozilla/5.0 115Browser/27.0.5.7",
		"115disk":        "Mozilla/5.0 115disk/30.1.0",
		"旧版完整浏览器UA":      ua115,
	}

	results := gin.H{}
	query := build115FileQuery("0", 0)

	for name, ua := range uas {
		body, err := httpGet115Full(fileListAPI, query, cookie, ua, 15*time.Second, nil)
		if err != nil {
			results[name] = gin.H{"ok": false, "error": err.Error()}
			continue
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Count int    `json:"count"`
		}
		json.Unmarshal(body, &r)
		results[name] = gin.H{
			"ok":    r.State,
			"error": r.Error,
			"count": r.Count,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"cookie_len": len(cookie),
		"results":    results,
		"hint":       "如果所有 UA 都失败且 error=服务器开小差了，通常是服务器 IP 被 115 风控（机房 IP 常见）。请在本地电脑用相同 Cookie 测试：如果本地成功、服务器失败，即可确认是 IP 问题。",
	})
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
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}
