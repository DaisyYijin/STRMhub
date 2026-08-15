package main

import (
	"fmt"
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

	// 命令行参数：--reset-admin 重置管理员密码（只删 auth.yaml，不丢其他配置）
	if len(os.Args) > 1 && os.Args[1] == "--reset-admin" {
		if err := cfg.ResetAuth(); err != nil {
			fmt.Printf("重置失败（可能尚未注册）: %v\n", err)
		} else {
			fmt.Println("管理员账号已重置，所有配置已保留。请重新启动程序并注册新账号。")
		}
		return
	}

	log.Printf("StrmHub 启动中... 管理端口:%d 代理端口:%d", cfg.Port, cfg.ProxyPort)

	// 确保配置目录存在
	if err := cfg.EnsureConfigDir(); err != nil {
		log.Fatalf("创建配置目录失败: %v", err)
	}

	// 确保数据目录存在
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	log.Println(cfg.ConfigSummary())

	// 确保日志目录存在
	logDir := "/logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("创建日志目录失败: %v，日志仅输出到控制台", err)
	} else {
		logFile, err := os.OpenFile(filepath.Join(logDir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			defer logFile.Close()
			log.SetOutput(io.MultiWriter(os.Stdout, logFile))
		}
	}

	// 初始化数据库（分类策略、洗版策略、同步记录）
	db, err := model.InitDB(filepath.Join(cfg.DataDir, "strmhub.db"))
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 检查是否首次部署（检查 auth.yaml 是否存在）
	if cfg.IsAuthExists() {
		log.Println("系统已初始化，显示登录页")
	} else {
		log.Println("首次部署，显示注册页")
	}

	// 初始化默认二级分类和洗版策略
	if err := model.InitDefaultCategories(db); err != nil {
		log.Printf("初始化默认二级分类失败: %v", err)
	}
	if err := model.InitDefaultWashRules(db); err != nil {
		log.Printf("初始化默认洗版策略失败: %v", err)
	}

	// 启动 Gin（不用 gin.Default：其自带的请求访问日志每个 HTTP 请求一行，噪音大）
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

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
