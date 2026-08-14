package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// CheckStorage 检测 Cookie 可用性（占位：后续接入 115 API 校验）
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
	// TODO: 调用 115 网盘 API 校验 Cookie，返回账号名与容量
	c.JSON(http.StatusOK, gin.H{
		"username": "",
		"capacity": "",
		"valid":    false,
		"message":  "Cookie 检测功能待接入 115 API",
	})
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
