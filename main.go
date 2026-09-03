package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

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
// （app.log → app.log.1 → .2 → .3，最旧的丢弃）
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
	// 子命令：update-finish <旧容器ID> <新容器ID>
	// 由「更新辅助容器」执行（同一镜像 + docker.sock），负责停旧容器→启动新容器→清理。
	// 更新流程里主容器不能停自己（进程会被杀，后续步骤无法执行），收尾必须由独立进程完成。
	if len(os.Args) >= 4 && os.Args[1] == "update-finish" {
		api.RunUpdateFinish(os.Args[2], os.Args[3])
		return
	}

	// 初始化配置
	cfg := config.Load()

	// 命令行参数：--reset-admin 重置管理员（只删 auth.yaml，不丢其他配置）
	if len(os.Args) > 1 && os.Args[1] == "--reset-admin" {
		if err := cfg.ResetAuth(); err != nil {
			fmt.Printf("重置失败（可能尚无账号文件）: %v\n", err)
		} else {
			fmt.Println("管理员账号文件已删除，所有配置已保留。重启后按环境变量 AUTH_USER/AUTH_PASSWORD 重建，未配置则生成随机密码（见启动日志）。")
		}
		return
	}

	log.Printf("StrmHub 启动中... 版本:%s 管理端口:%d 代理端口:%d", BuildSHA, cfg.Port, cfg.ProxyPort)

	// 确保配置目录存在
	if err := cfg.EnsureConfigDir(); err != nil {
		log.Fatalf("创建配置目录失败: %v", err)
	}

	// 管理员账号：环境变量 AUTH_USER/AUTH_PASSWORD 优先（未配置且无历史账号
	// 时自动生成随机密码并打印日志）。网页注册功能已移除
	if err := cfg.EnsureAdmin(); err != nil {
		log.Printf("[账号] ○ 管理员账号初始化失败（登录不可用，请检查权限）: %v", err)
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

	api.SetVersion(BuildSHA)

	// 启动 Gin（不用 gin.Default：其自带的请求访问日志每个 HTTP 请求一行，噪音大）
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// 不信任任何代理头：ClientIP 一律取实际连接地址。默认（信任所有代理）下
	// X-Forwarded--For 可被客户端伪造，登录防爆破会被轮换 XFF 绕过。
	// 若未来部署到反代之后，把反代 IP 加进来即可
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery())

	// CORS
	r.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		// 收紧请求头白名单：API 只用 Authorization + JSON；
		// 通配 * 会放行任意自定义头
		AllowHeaders: []string{"Authorization", "Content-Type"},
	}))

	// API 路由
	apiGroup := r.Group("/api")
	api.SetupRoutes(apiGroup, db, cfg)

	// 静态文件（前端）：no-cache = 协商缓存（ETag 校验，未变返回 304 不重下）。
	// 之前靠 index.html 里手写的 ?v=N 版本号失效缓存——更新后浏览器仍可能
	// 用旧 JS 调新接口/碰已删除的元素（登录页报错即此因），改为服务端
	// 强制校验，一劳永逸。
	// 用中间件而非精确路由：r.Static 注册 /css/*filepath 通配，gin 不允许
	// 与 /css/style.css 精确段共存（路由树冲突 → 启动 panic）
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/css/") ||
			strings.HasPrefix(c.Request.URL.Path, "/js/") ||
			strings.HasPrefix(c.Request.URL.Path, "/vendor/") {
			// no-store：浏览器完全不缓存。文件很小（app.js ~120KB），
			// 换来"更新必定生效"，杜绝旧脚本调用新接口的排障灾难
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})
	r.Static("/css", "./web/css")
	r.Static("/js", "./web/js")
	r.Static("/vendor", "./web/vendor") // CodeMirror 等第三方前端库
	r.StaticFile("/cms-115.png", "./web/cms-115.png")
	// index.html 禁用启发式缓存：升级后浏览器总是重新校验，避免页面拿到旧 HTML 搭配新 ?v= 资产
	serveIndex := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.File("./web/index.html")
	}
	r.GET("/", serveIndex)
	r.NoRoute(serveIndex)

	// 启动302代理（独立端口）
	go api.StartProxy(db, cfg)

	// 启动观影门户（6688：海报墙 + 网页播放）
	go api.StartPortal(cfg)

	// 企微聊天底栏菜单默认自动生成（自动整理 / 增量同步）
	go api.WecomMenuAutoEnsure()

	// 优雅退出：docker stop / 自更新收尾发 SIGTERM——直接杀进程会把
	// 「115 已搬移、台账未写」的中间态留在云端（下次去重/洗版判定失据），
	// 防抖队列里的入库通知也全部丢失。停 worker → 冲刷通知 → 留收尾窗口
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("收到信号 %v，优雅退出中（停止后台任务、冲刷待发通知）…", sig)
		api.ShutdownWorkers()
		time.Sleep(3 * time.Second) // 在途 115 请求收尾窗口（compose stop_grace_period 应≥此值）
		log.Printf("✓ 退出完成")
		os.Exit(0)
	}()

	log.Printf("管理后台已启动（端口 %d）", cfg.Port)
	if err := r.Run(":" + cfg.PortStr()); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}
