package api

// CloudDrive2（CD2）只读接入：
// CD2 把几十种网盘（115/阿里/夸克/百度/天翼/OneDrive/S3…）统一成一个 gRPC 服务，
// 本模块把它当作「万能网盘后端」——扫描 CD2 内目录树为视频文件生成 STRM
// （内容指向本机 302 代理 /cd2/{base64(完整路径)}），播放时取直链并 302：
//   - 优先网盘原始直链（仅当无 UA/请求头要求时，播放器可直连，速度最快）
//   - 否则 302 到 CD2 中转地址（{SCHEME}/{HOST} 替换后），由 CD2 转发流量，
//     对任何网盘可用且不受 UA 限制（走 CD2 所在机器的上行带宽）
//
// 一期只做只读（列目录/取直链），转存、离线等写操作后续版本再接。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strmhub/internal/cd2"

	"github.com/gin-gonic/gin"
)

type cd2Cfg struct {
	Endpoint     string `json:"endpoint"` // host:port 或 http(s)://host:port，默认端口 19798
	Username     string `json:"username"`
	Password     string `json:"password"`
	RootPath     string `json:"root_path"`     // CD2 内的媒体库根（扫描根 / 整理目标根）
	LocalPath    string `json:"local_path"`    // STRM 输出目录
	PreferDirect bool   `json:"prefer_direct"` // 优先网盘直链（无 UA 要求时），否则始终走 CD2 中转
	OrgEnabled   bool   `json:"org_enabled"`   // 实时监控整理开关
	OrgPending   string `json:"org_pending"`   // 监控（待整理）目录，CD2 绝对路径
}

func (h *Handler) loadCd2Cfg() cd2Cfg {
	c := cd2Cfg{}
	if v := h.Config.GetSetting("cd2"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (h *Handler) saveCd2Cfg(c cd2Cfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("cd2", string(b))
}

// ==================== 客户端缓存（配置变更时自动重建） ====================

var (
	cd2ClientMu  sync.Mutex
	cd2ClientVal *cd2.Client
	cd2ClientKey string
)

func (h *Handler) cd2Client() (*cd2.Client, error) {
	cfg := h.loadCd2Cfg()
	if strings.TrimSpace(cfg.Endpoint) == "" || cfg.Username == "" {
		return nil, fmt.Errorf("请先配置 CD2 服务地址与账号")
	}
	target, err := cd2GrpcTarget(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	key := target + "\x00" + cfg.Username + "\x00" + cfg.Password
	cd2ClientMu.Lock()
	defer cd2ClientMu.Unlock()
	if cd2ClientVal == nil || cd2ClientKey != key {
		if cd2ClientVal != nil {
			_ = cd2ClientVal.Close()
		}
		cd2ClientVal = cd2.NewClient(target, cfg.Username, cfg.Password)
		cd2ClientKey = key
	}
	return cd2ClientVal, nil
}

// ==================== 端点地址规范化（可测试） ====================

// cd2GrpcTarget 把用户输入规范化为 grpc target（host:port）。
// 支持带 http(s):// 前缀；缺端口时补默认 19798
func cd2GrpcTarget(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("CD2 地址为空")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("CD2 地址无效: %s", raw)
		}
		s = u.Host
	}
	if !strings.Contains(s, ":") || strings.HasSuffix(s, "]") {
		s += ":19798"
	}
	return s, nil
}

// cd2HTTPBase 返回 CD2 服务对外的基础 URL（scheme://host:port），用于拼中转地址
func cd2HTTPBase(raw string) (string, string, error) {
	s := strings.TrimSpace(raw)
	scheme := "http"
	if s == "" {
		return "", "", fmt.Errorf("CD2 地址为空")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", "", fmt.Errorf("CD2 地址无效: %s", raw)
		}
		if u.Scheme == "https" {
			scheme = "https"
		}
		s = u.Host
	}
	if !strings.Contains(s, ":") || strings.HasSuffix(s, "]") {
		s += ":19798"
	}
	return scheme, s, nil
}

// cd2BuildProxyURL 把 downloadUrlPath 模板替换成 CD2 服务器上的完整 URL。
// 模板形如 /static/{SCHEME}/{HOST}/{PREVIEW}/path?token=abc，
// {SCHEME}/{HOST} 用配置的 CD2 地址填充，{PREVIEW} 固定 false（原始文件）
func cd2BuildProxyURL(endpoint, proxyPath string) string {
	scheme, host, err := cd2HTTPBase(endpoint)
	if err != nil {
		scheme, host = "http", strings.TrimSpace(endpoint)
	}
	if !strings.HasPrefix(proxyPath, "/") {
		proxyPath = "/" + proxyPath
	}
	r := strings.NewReplacer("{SCHEME}", scheme, "{HOST}", host, "{PREVIEW}", "false")
	return scheme + "://" + host + r.Replace(proxyPath)
}

// cd2PickTarget 选择 302 目标：优先直链（必须无 UA/请求头要求），否则 CD2 中转
func cd2PickTarget(endpoint string, preferDirect bool, info *cd2.URLInfo) string {
	if preferDirect && info != nil && info.DirectURL != "" && info.UserAgent == "" && len(info.ExtraHeaders) == 0 {
		return info.DirectURL
	}
	if info == nil || info.ProxyPath == "" {
		return ""
	}
	return cd2BuildProxyURL(endpoint, info.ProxyPath)
}

// ==================== HTTP handlers ====================

// Cd2GetConfig GET /cd2/config
func (h *Handler) Cd2GetConfig(c *gin.Context) {
	cfg := h.loadCd2Cfg()
	if cfg.Password != "" {
		cfg.Password = settingMask
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

// Cd2SaveConfig POST /cd2/config（密码掩码回传 = 保持旧值）
func (h *Handler) Cd2SaveConfig(c *gin.Context) {
	var req cd2Cfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	old := h.loadCd2Cfg()
	if req.Password == settingMask {
		req.Password = old.Password
	}
	if _, err := cd2GrpcTarget(req.Endpoint); req.Endpoint != "" && err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.saveCd2Cfg(req)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// Cd2Test POST /cd2/test：登录并列出根目录（根目录即各网盘挂载点）
func (h *Handler) Cd2Test(c *gin.Context) {
	cl, err := h.cd2Client()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	files, err := cl.ListDir(ctx, "/")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var drives []string
	for _, f := range files {
		if f.IsDir && len(drives) < 8 {
			drives = append(drives, f.Name)
		}
	}
	msg := fmt.Sprintf("连接成功（%dms），CD2 根目录 %d 项：%s",
		time.Since(start).Milliseconds(), len(files), strings.Join(drives, "、"))
	c.JSON(http.StatusOK, gin.H{"message": msg})
}

// Cd2Dirs GET /cd2/dirs?path=/：目录浏览（只返回文件夹），供前端目录选择器
func (h *Handler) Cd2Dirs(c *gin.Context) {
	pathParam := strings.TrimSpace(c.Query("path"))
	if pathParam == "" {
		pathParam = "/"
	}
	cl, err := h.cd2Client()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	files, err := cl.ListDir(ctx, pathParam)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	dirs := []gin.H{}
	for _, f := range files {
		if f.IsDir {
			dirs = append(dirs, gin.H{"name": f.Name, "path": f.Path})
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": dirs, "path": pathParam, "count": len(dirs)})
}

// ==================== 扫描生成 STRM ====================

var cd2ScanMu sync.Mutex

// Cd2Scan POST /cd2/scan：BFS 遍历扫描根目录，为视频文件生成 STRM
func (h *Handler) Cd2Scan(c *gin.Context) {
	cfg := h.loadCd2Cfg()
	if cfg.Endpoint == "" || cfg.Username == "" || cfg.RootPath == "" || cfg.LocalPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先完成配置（地址账号 + 扫描目录 + STRM 输出目录）"})
		return
	}
	if !cd2ScanMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "CD2 扫描已在进行中"})
		return
	}
	go func() {
		defer cd2ScanMu.Unlock()
		created, skipped, failed := h.cd2WalkAndStrm()
		log.Printf("[CD2] 扫描完成：新增 STRM %d，跳过 %d，失败 %d", created, skipped, failed)
		NotifyMessage("▤ CD2 扫描完成",
			fmt.Sprintf("新增 STRM：%d\n跳过（已存在）：%d\n失败：%d", created, skipped, failed))
	}()
	c.JSON(http.StatusOK, gin.H{"message": "扫描已开始，结果可看日志与通知"})
}

func (h *Handler) cd2WalkAndStrm() (created, skipped, failed int) {
	cfg := h.loadCd2Cfg()
	domain, format, keepExt, skipExist := h.getStrmConfig()
	if domain == "" {
		domain = "http://127.0.0.1:" + h.Config.ProxyPortStr()
		log.Printf("[CD2] ○ 未配置代理域名，STRM 暂用本机代理地址 %s", domain)
	}
	cl, err := h.cd2Client()
	if err != nil {
		log.Printf("[CD2] ✗ %v", err)
		return 0, 0, 1
	}
	root := "/" + strings.Trim(cfg.RootPath, "/")

	type dirJob struct {
		path string
		rel  string // 相对扫描根的路径
	}
	queue := []dirJob{{path: root}}
	dirs := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		files, err := cl.ListDir(ctx, cur.path)
		cancel()
		if err != nil {
			failed++
			log.Printf("[CD2] ✗ 列目录失败 %q: %v", cur.rel, err)
			continue
		}
		dirs++
		if dirs%10 == 0 {
			log.Printf("[CD2] 扫描中：已处理 %d 个目录，新增 %d 个 STRM", dirs, created)
		}
		for _, f := range files {
			if f.IsDir {
				queue = append(queue, dirJob{path: f.Path, rel: joinRelPath(cur.rel, f.Name)})
				continue
			}
			if !isVideoName(f.Name) {
				continue
			}
			written, err := writeStrmCd2(cfg.LocalPath, domain, format, keepExt, skipExist, cur.rel, f.Name, f.Path)
			switch {
			case err != nil:
				failed++
				log.Printf("[CD2] ✗ STRM 失败 %s/%s: %v", cur.rel, f.Name, err)
			case written:
				created++
			default:
				skipped++
			}
		}
	}
	return
}

// writeStrmCd2 与 123 的 writeStrm123 同构：本地目录保持 CD2 目录结构，
// STRM 内容 = {domain}/cd2/{base64url(完整路径)}[.ext]；返回是否新写入。
// 用完整路径（而非相对路径）编码，之后改扫描根不影响旧 STRM 的解析
func writeStrmCd2(localRoot, domain, format string, keepExt, skipExist bool, relDir, name, fullPath string) (bool, error) {
	base := strings.TrimRight(domain, "/")
	idPart := base64.RawURLEncoding.EncodeToString([]byte(fullPath))
	if keepExt {
		idPart += pathExt(name)
	}
	var streamURL string
	if format == "pick_code" {
		streamURL = fmt.Sprintf("%s/cd2/%s", base, idPart)
	} else {
		streamURL = fmt.Sprintf("%s/cd2/%s?/%s", base, idPart, name)
	}
	dir := filepath.Join(localRoot, filepath.FromSlash(relDir))
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return false, err
	}
	strmPath := filepath.Join(dir, name+".strm")
	if skipExist {
		if _, err := os.Stat(strmPath); err == nil {
			return false, nil
		}
	}
	if err := os.WriteFile(strmPath, []byte(streamURL), 0o666); err != nil {
		return false, err
	}
	return true, nil
}

// ==================== 播放 302 ====================

// handleCd2Redirect /cd2/{base64(完整路径)}[.ext][/{name}] → 取播放地址 302。
// 直链（无 UA 要求）或 CD2 中转地址二选一；按 expiresIn 缓存（上限 10 分钟）
func (h *Handler) handleCd2Redirect(c *gin.Context) {
	id := c.Param("id")
	if i := strings.LastIndex(id, "."); i > 0 {
		id = id[:i] // keepExt 追加的扩展名（base64url 不含点，切分安全）
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid cd2 path")
		return
	}
	fullPath := string(raw)
	if !strings.HasPrefix(fullPath, "/") {
		c.String(http.StatusBadRequest, "invalid cd2 path")
		return
	}
	// 与 /d/ 同级的外部端点：沿用同一套 IP 限流（20 次/分），防直链遍历
	if !proxyRateAllow(c.ClientIP()) {
		c.String(http.StatusTooManyRequests, "rate limit")
		return
	}

	cacheKey := "cd2:" + fullPath
	downloadCacheMu.Lock()
	cached, ok := downloadLinkCache[cacheKey]
	downloadCacheMu.Unlock()
	if ok && time.Now().Before(cached.Expiry) {
		c.Redirect(http.StatusFound, cached.URL)
		return
	}

	cl, err := h.cd2Client()
	if err != nil {
		c.String(http.StatusBadRequest, "%v", err)
		return
	}
	cfg := h.loadCd2Cfg()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	info, err := cl.DownloadURL(ctx, fullPath, cfg.PreferDirect)
	if err != nil {
		log.Printf("[CD2] ✗ 302 代理取播放地址失败 %q: %v", fullPath, err)
		c.String(http.StatusBadGateway, "获取 CD2 播放地址失败: %v", err)
		return
	}
	target := cd2PickTarget(cfg.Endpoint, cfg.PreferDirect, info)
	if target == "" {
		c.String(http.StatusBadGateway, "CD2 未返回可用的播放地址")
		return
	}
	ttl := 5 * time.Minute
	if info.HasExpires {
		if d := time.Duration(info.ExpiresIn) * time.Second; d > 0 && d < 10*time.Minute {
			ttl = d
		}
	}
	downloadCacheMu.Lock()
	downloadLinkCache[cacheKey] = downloadCacheEntry{URL: target, Expiry: time.Now().Add(ttl)}
	downloadCacheMu.Unlock()
	c.Redirect(http.StatusFound, target)
}
