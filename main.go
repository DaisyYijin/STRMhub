package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strmhub/internal/api"
	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	// 初始化配置
	cfg := config.Load()
	log.Printf("StrmHub 启动中... 管理端口:%d 代理端口:%d", cfg.Port, cfg.ProxyPort)

	// 确保数据目录存在
	dataDir := filepath.Join(cfg.DataDir)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	// 确保日志目录存在
	logDir := "/logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("创建日志目录失败: %v，日志仅输出到控制台", err)
	} else {
		// 日志同时输出到控制台和文件
		logFile, err := os.OpenFile(filepath.Join(logDir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			defer logFile.Close()
			log.SetOutput(io.MultiWriter(os.Stdout, logFile))
		}
	}

	// 初始化数据库
	db, err := model.InitDB(filepath.Join(dataDir, "strmhub.db"))
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 检查是否首次部署（无管理员）
	initialized, err := model.IsInitialized(db)
	if err != nil {
		log.Fatalf("检查初始化状态失败: %v", err)
	}
	if initialized {
		log.Println("系统已初始化，显示登录页")
	} else {
		log.Println("首次部署，显示注册页")
	}

	// 初始化默认二级分类和洗版策略（CMS 风格）
	if err := model.InitDefaultCategories(db); err != nil {
		log.Printf("初始化默认二级分类失败: %v", err)
	}
	if err := model.InitDefaultWashRules(db); err != nil {
		log.Printf("初始化默认洗版策略失败: %v", err)
	}

	// 启动 Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// CORS
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"*"},
		AllowCredentials: true,
	}))

	// API 路由
	apiGroup := r.Group("/api")
	api.SetupRoutes(apiGroup, db, cfg)

	// 静态文件（前端）
	r.Static("/css", "./web/css")
	r.Static("/js", "./web/js")
	r.StaticFile("/cms-115.png", "./web/cms-115.png")
	r.StaticFile("/", "./web/index.html")
	r.NoRoute(func(c *gin.Context) {
		c.File("./web/index.html")
	})

	// 启动302代理（独立端口）
	go api.StartProxy(db, cfg)

	log.Printf("StrmHub 管理后台: http://localhost:%d", cfg.Port)
	if err := r.Run(":" + cfg.PortStr()); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}
