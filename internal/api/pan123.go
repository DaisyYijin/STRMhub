package api

// 123 云盘（123pan）官方开放平台接入：
// 凭证为开发者平台的 clientID/clientSecret，换取 access_token（约 30 天，
// 自动续期）。流程：扫描目录树（/api/v2/file/list）→ 为视频文件生成 STRM
// （内容指向本机 302 代理 /123/{fileID}）→ 播放时调
// /api/v1/file/download_info 取直链并 302。
//
// 官方限制（务必向用户提示）：
//   - 目标文件夹须在 123 网盘里开启「直链空间」，根目录不支持直链
//   - 免费账号 download_info 自用下载流量 1GB/天，超限返回 code 5113
//   - QPS：列表 15、download_info 较低（客户端统一 150ms 节流）

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
)

const pan123Base = "https://open-api.123pan.com"

type pan123Cfg struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Token        string `json:"token"`      // 扫码登录拿到的网页 token（30 天，可随时重扫）
	TokenExp     string `json:"token_exp"`  // token 预期过期时间
	TargetID     string `json:"target_id"`  // 扫描根目录 ID（123 网盘里开启直链空间的目录）
	LocalPath    string `json:"local_path"` // STRM 输出目录
}

func (h *Handler) loadPan123Cfg() pan123Cfg {
	c := pan123Cfg{}
	if v := h.Config.GetSetting("123pan"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (h *Handler) savePan123Cfg(c pan123Cfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("123pan", string(b))
}

// ==================== token 与请求 ====================

var (
	pan123TokenMu  sync.Mutex
	pan123TokenVal string
	pan123TokenExp time.Time
	pan123Next     time.Time // 全局节流：两次 API 请求最小间隔 150ms
	pan123NextMu   sync.Mutex
	pan123ScanMu   sync.Mutex
)

func pan123ThrottleWait() {
	pan123NextMu.Lock()
	defer pan123NextMu.Unlock()
	now := time.Now()
	if pan123Next.After(now) {
		time.Sleep(pan123Next.Sub(now))
		pan123Next = time.Now().Add(150 * time.Millisecond)
	} else {
		pan123Next = now.Add(150 * time.Millisecond)
	}
}

// pan123FetchToken 用 clientID/clientSecret 换 access_token（约 30 天有效）
func (h *Handler) pan123FetchToken() (string, error) {
	cfg := h.loadPan123Cfg()
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", fmt.Errorf("未配置 clientID/clientSecret")
	}
	body, _ := json.Marshal(map[string]string{"clientID": cfg.ClientID, "clientSecret": cfg.ClientSecret})
	req, err := http.NewRequest(http.MethodPost, pan123Base+"/api/v1/access_token", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Platform", "open_platform")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken string `json:"accessToken"`
			ExpiredAt   string `json:"expiredAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("token 响应解析失败: %s", truncateStr(string(raw), 150))
	}
	if env.Code != 0 && env.Code != 200 {
		return "", fmt.Errorf("获取 token 失败: code=%d %s", env.Code, env.Message)
	}
	if env.Data.AccessToken == "" {
		return "", fmt.Errorf("token 响应为空")
	}
	// expiredAt 缺省按 30 天处理；本地提前 1 天续期
	exp := time.Now().Add(29 * 24 * time.Hour)
	if t, err := time.Parse("2006-01-02 15:04:05", env.Data.ExpiredAt); err == nil {
		exp = t
	}
	pan123TokenMu.Lock()
	pan123TokenVal, pan123TokenExp = env.Data.AccessToken, exp
	pan123TokenMu.Unlock()
	log.Printf("[123盘] ✓ access_token 获取成功（有效期至 %s）", exp.Format("2006-01-02"))
	return env.Data.AccessToken, nil
}

// pan123Token 取缓存 token，临期自动换新。
// 优先扫码登录的网页 token（30 天，过期需重扫），其次开放平台凭证
func (h *Handler) pan123Token() (string, error) {
	pan123TokenMu.Lock()
	tok, exp := pan123TokenVal, pan123TokenExp
	pan123TokenMu.Unlock()
	if tok != "" && time.Now().Before(exp.Add(-24*time.Hour)) {
		return tok, nil
	}
	cfg := h.loadPan123Cfg()
	if cfg.Token != "" {
		exp := time.Now().Add(29 * 24 * time.Hour)
		if t, err := time.Parse("2006-01-02 15:04", cfg.TokenExp); err == nil {
			exp = t
		}
		if time.Now().Before(exp.Add(-24 * time.Hour)) {
			return cfg.Token, nil
		}
		return "", fmt.Errorf("123 登录已过期（token 有效期 30 天），请到「账号管理 → 123 账号」重新扫码")
	}
	return h.pan123FetchToken()
}

// pan123API 调开放平台接口（自动带 token；401/419 重取一次，429 退避重试）
func (h *Handler) pan123API(method, path string, query url.Values, out any) error {
	call := func(token string) ([]byte, int, error) {
		pan123ThrottleWait()
		u := pan123Base + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		req, err := http.NewRequest(method, u, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Platform", "open_platform")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return raw, resp.StatusCode, nil
	}

	token, err := h.pan123Token()
	if err != nil {
		return err
	}
	raw, status, err := call(token)
	if err != nil {
		return err
	}
	// token 失效 → 强制换新重试一次（仅开放平台凭证可自动续；扫码 token 过期报错）
	if status == 401 || status == 419 {
		pan123TokenMu.Lock()
		pan123TokenVal = ""
		pan123TokenMu.Unlock()
		cfg := h.loadPan123Cfg()
		if cfg.Token != "" && cfg.ClientID == "" {
			return fmt.Errorf("123 登录已失效，请到「账号管理 → 123 账号」重新扫码")
		}
		if token, err = h.pan123FetchToken(); err != nil {
			return err
		}
		raw, status, err = call(token)
		if err != nil {
			return err
		}
	}
	// 限流 → 退避重试两次
	for i := 0; i < 2 && status == 429; i++ {
		time.Sleep(time.Duration(i+1) * time.Second)
		raw, status, err = call(token)
		if err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", status, truncateStr(string(raw), 150))
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("响应非 JSON: %s", truncateStr(string(raw), 150))
	}
	if env.Code != 0 && env.Code != 200 {
		return &pan123APIError{Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

type pan123APIError struct {
	Code    int
	Message string
}

func (e *pan123APIError) Error() string {
	if e.Code == 5113 {
		return "今日下载流量已用完（免费账号自用流量 1GB/天，开通会员可获得更多流量）"
	}
	return fmt.Sprintf("code=%d %s", e.Code, e.Message)
}

// ==================== 文件列表与扫描 ====================

type pan123File struct {
	FileID   int64  `json:"fileId"`
	Filename string `json:"filename"`
	Type     int    `json:"type"` // 0-文件 1-文件夹
	Size     int64  `json:"size"`
	Status   int    `json:"status"` // 大于 100 为审核驳回
	Trashed  int    `json:"trashed"`
}

// pan123ListDir 列出一个目录的全部子项（lastFileId 游标翻页，过滤回收站）
func (h *Handler) pan123ListDir(dirID int64) ([]pan123File, error) {
	var all []pan123File
	last := int64(0)
	for {
		q := url.Values{}
		q.Set("parentFileId", strconv.FormatInt(dirID, 10))
		q.Set("limit", "100")
		if last > 0 {
			q.Set("lastFileId", strconv.FormatInt(last, 10))
		}
		var data struct {
			LastFileID int64        `json:"lastFileId"`
			FileList   []pan123File `json:"fileList"`
		}
		if err := h.pan123API(http.MethodGet, "/api/v2/file/list", q, &data); err != nil {
			return all, err
		}
		for _, f := range data.FileList {
			if f.Trashed == 0 {
				all = append(all, f)
			}
		}
		if data.LastFileID <= 0 { // -1 = 最后一页
			return all, nil
		}
		last = data.LastFileID
	}
}

// pan123DownloadInfo 获取文件下载直链（直链有时效，即取即用）
func (h *Handler) pan123DownloadInfo(fileID int64) (string, error) {
	q := url.Values{}
	q.Set("fileId", strconv.FormatInt(fileID, 10))
	var data struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := h.pan123API(http.MethodGet, "/api/v1/file/download_info", q, &data); err != nil {
		return "", err
	}
	if data.DownloadURL == "" {
		return "", fmt.Errorf("直链为空（目录是否开启直链空间？）")
	}
	return data.DownloadURL, nil
}

// Pan123Scan POST /pan123/scan：BFS 遍历目标目录，为视频文件生成 STRM
func (h *Handler) Pan123Scan(c *gin.Context) {
	cfg := h.loadPan123Cfg()
	if cfg.ClientID == "" || cfg.TargetID == "" || cfg.LocalPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先完成配置（凭证 + 扫描目录 + STRM 输出目录）"})
		return
	}
	if !pan123ScanMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "123 云盘扫描已在进行中"})
		return
	}
	go func() {
		defer pan123ScanMu.Unlock()
		created, skipped, failed := h.pan123WalkAndStrm()
		log.Printf("[123盘] 扫描完成：新增 STRM %d，跳过 %d，失败 %d", created, skipped, failed)
		NotifyMessage("▤ 123 云盘扫描完成",
			fmt.Sprintf("新增 STRM：%d\n跳过（已存在）：%d\n失败：%d", created, skipped, failed))
	}()
	c.JSON(http.StatusOK, gin.H{"message": "扫描已开始，结果可看日志与通知"})
}

func (h *Handler) pan123WalkAndStrm() (created, skipped, failed int) {
	cfg := h.loadPan123Cfg()
	domain, format, keepExt, skipExist := h.getStrmConfig()
	if domain == "" {
		domain = "http://127.0.0.1:" + h.Config.ProxyPortStr()
		log.Printf("[123盘] ○ 未配置代理域名，STRM 暂用本机代理地址 %s", domain)
	}
	rootID, _ := strconv.ParseInt(strings.TrimSpace(cfg.TargetID), 10, 64)

	type dirJob struct {
		id  int64
		rel string // 相对扫描根的路径
	}
	queue := []dirJob{{id: rootID}}
	dirs := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		files, err := h.pan123ListDir(cur.id)
		if err != nil {
			log.Printf("[123盘] ✗ 列目录失败 %q: %v", cur.rel, err)
			failed++
			continue
		}
		dirs++
		if dirs%10 == 0 {
			log.Printf("[123盘] 扫描中：已处理 %d 个目录，新增 %d 个 STRM", dirs, created)
		}
		for _, f := range files {
			if f.Type == 1 {
				queue = append(queue, dirJob{id: f.FileID, rel: joinRelPath(cur.rel, f.Filename)})
				continue
			}
			// 审核驳回/被处罚文件拿不到直链，直接跳过
			if f.Status > 100 || !isVideoName(f.Filename) {
				continue
			}
			written, err := writeStrm123(cfg.LocalPath, domain, format, keepExt, skipExist, cur.rel, f.Filename, f.FileID)
			switch {
			case err != nil:
				failed++
				log.Printf("[123盘] ✗ STRM 失败 %s/%s: %v", cur.rel, f.Filename, err)
			case written:
				created++
			default:
				skipped++
			}
		}
	}
	return
}

func isVideoName(name string) bool {
	return classifyFile(name) == FileTypeVideo
}

func joinRelPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// writeStrm123 与 115 的 writeStrm 同构：本地目录保持网盘结构，
// STRM 内容 = {domain}/123/{fileID}[.ext]；返回是否新写入
func writeStrm123(localRoot, domain, format string, keepExt, skipExist bool, relDir, name string, fileID int64) (bool, error) {
	base := strings.TrimRight(domain, "/")
	idPart := strconv.FormatInt(fileID, 10)
	if keepExt {
		idPart += pathExt(name)
	}
	var streamURL string
	if format == "pick_code" {
		streamURL = fmt.Sprintf("%s/123/%s", base, idPart)
	} else {
		streamURL = fmt.Sprintf("%s/123/%s?/%s", base, idPart, name)
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

// ==================== 扫码登录（login.123pan.com 网页接口） ====================

const pan123LoginBase = "https://login.123pan.com"

// pan123LoginGet 调登录域名接口（带网页 UA/Origin）
func pan123LoginGet(path string, query url.Values) (map[string]any, error) {
	u := pan123LoginBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Origin", "https://www.123pan.com")
	req.Header.Set("Referer", "https://www.123pan.com/")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("响应非 JSON: %s", truncateStr(string(raw), 120))
	}
	var data map[string]any
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &data)
	}
	return data, nil
}

// Pan123Qrcode POST /pan123/qrcode：生成扫码登录二维码（PNG data URL）
func (h *Handler) Pan123Qrcode(c *gin.Context) {
	data, err := pan123LoginGet("/api/user/qr-code/generate", nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取二维码失败: " + err.Error()})
		return
	}
	uniID, _ := data["uniID"].(string)
	baseURL, _ := data["url"].(string)
	if uniID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "二维码响应缺少 uniID"})
		return
	}
	// 二维码内容格式来自 p123client：保留 .html 页面 + 完整参数，
	// 缺参数时手机扫码会当普通网页打开（跳到下载页而不是登录确认）
	content := baseURL + "?env=production&uniID=" + url.QueryEscape(uniID) + "&source=123pan&type=login"
	png, err := qrcode.Encode(content, qrcode.Medium, 220)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成二维码图片失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"uni_id": uniID, "qrcode": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}

// Pan123QrcodePoll GET /pan123/qrcode/poll?uni_id=
// loginStatus: 0 等待扫码 / 1 已扫码 / 2 已取消 / 3 已登录 / 4 已失效
func (h *Handler) Pan123QrcodePoll(c *gin.Context) {
	uniID := strings.TrimSpace(c.Query("uni_id"))
	if uniID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 uniID"})
		return
	}
	data, err := pan123LoginGet("/api/user/qr-code/result", url.Values{"uniID": {uniID}})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	status := 0
	if v, ok := data["loginStatus"].(float64); ok {
		status = int(v)
	}
	out := gin.H{"status": status}
	if status == 3 {
		token, _ := data["token"].(string)
		if token == "" {
			// token 字段位置不固定，兜底从整个 data 里正则提取
			raw, _ := json.Marshal(data)
			if m := regexp.MustCompile(`"token":"([^"]+)"`).FindSubmatch(raw); m != nil {
				token = string(m[1])
			}
		}
		if token == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "登录成功但未取到 token"})
			return
		}
		cfg := h.loadPan123Cfg()
		cfg.Token = token
		cfg.TokenExp = time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02 15:04")
		h.savePan123Cfg(cfg)
		out["message"] = "登录成功，token 有效期 30 天"
	}
	c.JSON(http.StatusOK, out)
}

// ==================== HTTP 处理器 ====================

// Pan123GetConfig GET /pan123/config
func (h *Handler) Pan123GetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": h.loadPan123Cfg()})
}

// Pan123SaveConfig POST /pan123/config
// 扫码 token 不经前端表单，保存时原值保留，避免被空值覆盖
func (h *Handler) Pan123SaveConfig(c *gin.Context) {
	var req pan123Cfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Token == "" {
		if old := h.loadPan123Cfg(); old.Token != "" {
			req.Token, req.TokenExp = old.Token, old.TokenExp
		}
	}
	h.savePan123Cfg(req)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// Pan123Test POST /pan123/test：验证登录态（扫码 token 或开放平台凭证均可）
func (h *Handler) Pan123Test(c *gin.Context) {
	if _, err := h.pan123Token(); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	files, err := h.pan123ListDir(0)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "token 正常但列目录失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("连接正常，根目录 %d 个子项", len(files))})
}

// Pan123CheckDir POST /pan123/checkdir {id}：校验目录 ID 可访问
func (h *Handler) Pan123CheckDir(c *gin.Context) {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(req.ID), 10, 64)
	files, err := h.pan123ListDir(id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("目录有效，含 %d 个子项", len(files))})
}
