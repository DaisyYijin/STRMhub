package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"strmhub/internal/api"
	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// BuildSHA 构建时由 CI 注入（-ldflags "-X main.BuildSHA=xxx"），
// 用于日志/UI 确认运行的是哪个提交（排查"更新没生效"类问题）
var BuildSHA = "dev"

// rotatingWriter 大小轮转日志写入器：超过 maxBytes 时切割
//（app.log → app.log.1 → .2 → .3，最旧的丢弃）
type rotatingWriter struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	size     int64
	maxBytes int64
	keep     int
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() {
	w.f.Close()
	for i := w.keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	os.Rename(w.path, w.path+".1")
	if f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		w.f = f
		w.size = 0
	}
}

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

	log.Printf("StrmHub 启动中... 版本:%s 管理端口:%d 代理端口:%d", BuildSHA, cfg.Port, cfg.ProxyPort)

	// 确保配置目录存在
	if err := cfg.EnsureConfigDir(); err != nil {
		log.Fatalf("创建配置目录失败: %v", err)
	}

	// 确保数据目录存在
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	log.Println(cfg.ConfigSummary())

	// 确保日志目录存在（app.log 大小轮转：超 10MB 切割，保留最近 3 份，
	// 防止长期运行无限追加撑爆磁盘）
	logDir := "/logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("创建日志目录失败: %v，日志仅输出到控制台", err)
	} else {
		logPath := filepath.Join(logDir, "app.log")
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			defer logFile.Close()
			rw := &rotatingWriter{f: logFile, path: logPath, maxBytes: 10 << 20, keep: 3}
			if st, serr := logFile.Stat(); serr == nil {
				rw.size = st.Size()
			}
			log.SetOutput(io.MultiWriter(os.Stdout, rw))
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
