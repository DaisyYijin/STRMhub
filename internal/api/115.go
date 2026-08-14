package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 115 扫码登录 ====================
//
// 参考 Cloud Media Sync / p115client 的扫码登录流程：
//   1. GET  https://qrcodeapi.115.com/api/1.0/web/1.0/token/           获取 uid/time/sign
//   2. GET  https://qrcodeapi.115.com/api/1.0/mac/1.0/qrcode?uid=...   获取二维码图片
//   3. GET  https://qrcodeapi.115.com/get/status/?uid=&time=&sign=     轮询扫码状态
//   4. POST https://passportapi.115.com/app/1.0/{app}/1.0/login/qrcode/ 获取 Cookie
//
// 状态码：0=等待扫码，1=已扫码待确认，2=已确认登录，-1=已过期，-2=已取消

const (
	tokenAPI     = "https://qrcodeapi.115.com/api/1.0/web/1.0/token/"
	qrcodeImgAPI = "https://qrcodeapi.115.com/api/1.0/mac/1.0/qrcode"
	statusAPI    = "https://qrcodeapi.115.com/get/status/"
	resultAPI    = "https://passportapi.115.com/app/1.0/%s/1.0/login/qrcode/"
)

// ua115Unified Cookie 通道统一 User-Agent（OpenList/115driver 生产验证组合）
// 115 会把 Cookie 会话与登录时的 UA 绑定：登录、列目录、检查、下载必须全程同一个 UA，
// 且必须是真实客户端 UA。"浏览器UA+115Browser后缀"的混合 UA 会被风控拒绝（服务器开小差了）。
func ua115Unified() string {
	return "Mozilla/5.0 115Browser/" + getAppVerCached()
}

// qrSession 保存 uid -> app 映射，供轮询时获取登录结果
type qrSession struct {
	app    string
	device string
}

var (
	qrMu       sync.RWMutex
	qrSessions = map[string]qrSession{}
)

// 前端设备类型 -> 115 app 短名
func mapDeviceToApp(device string) string {
	switch device {
	case "115ios":
		return "ios"
	case "115android":
		return "android"
	case "alipaymini":
		return "alipaymini"
	case "wechatmini":
		return "wechatmini"
	case "tv":
		return "tv"
	case "qandroid":
		return "qandroid"
	default:
		return "alipaymini"
	}
}

// deviceToUA 返回 Cookie 通道统一 UA
// 115 会话与 UA 绑定而非与设备绑定（OpenList/115driver 对所有 app 类型的 Cookie
// 都统一使用 "Mozilla/5.0 115Browser/{版本}" 访问 webapi），此处保持一致。
func deviceToUA(device string) string {
	return ua115Unified()
}

// appVer 缓存的 115 客户端版本号
var (
	appVerMu   sync.RWMutex
	appVerVal  = "36.0.0" // 默认值（115driver 库的同款默认）
	appVerTime time.Time  // 上次刷新时间
)

// getAppVerCached 获取 115 客户端版本号（1 小时缓存）
// 版本过旧会被 115 拒绝，需从官方接口动态获取
// 参考 115driver: ApiGetVersion = "https://appversion.115.com/1/web/1.0/api/chrome"
func getAppVerCached() string {
	appVerMu.RLock()
	if time.Since(appVerTime) < time.Hour && appVerVal != "" {
		v := appVerVal
		appVerMu.RUnlock()
		return v
	}
	appVerMu.RUnlock()

	ver := fetchAppVer()
	appVerMu.Lock()
	appVerVal = ver
	appVerTime = time.Now()
	appVerMu.Unlock()
	return ver
}

// fetchAppVer 从 115 官方接口获取当前版本号
func fetchAppVer() string {
	const api = "https://appversion.115.com/1/web/1.0/api/chrome"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(api)
	if err != nil {
		log.Printf("[115] 获取版本号失败: %v，使用默认 %s", err, appVerVal)
		return appVerVal
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return appVerVal
	}
	// 响应格式: {"state":true,"data":{"linux":{"version_code":"..."}...}}
	var result struct {
		State bool                          `json:"state"`
		Data  map[string]map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil || !result.State {
		return appVerVal
	}
	// 优先取 web/win 版本
	for _, key := range []string{"web", "win", "chrome"} {
		if v, ok := result.Data[key]["version_code"]; ok {
			if s, ok := v.(string); ok && s != "" {
				log.Printf("[115] 获取到最新版本号: %s", s)
				return s
			}
		}
	}
	return appVerVal
}

type qrTokenResp struct {
	Data struct {
		Uid  string `json:"uid"`
		Time int64  `json:"time"`
		Sign string `json:"sign"`
	} `json:"data"`
}

type qrStatusResp struct {
	Data struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	} `json:"data"`
}

type qrResultResp struct {
	State int `json:"state"`
	Data struct {
		UserName string                 `json:"user_name"`
		Cookie   map[string]interface{} `json:"cookie"`
	} `json:"data"`
}

// httpGetJSON 发起 GET 请求并返回响应体
func httpGetJSON(api string, query url.Values, timeout time.Duration) ([]byte, error) {
	full := api
	if len(query) > 0 {
		full = api + "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua115Unified())
	req.Header.Set("Referer", "https://115.com/")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// CreateQrCode 生成扫码登录二维码
// POST /storage/qrcode  body: { "type": "115", "device": "115android" }
func (h *Handler) CreateQrCode(c *gin.Context) {
	var req struct {
		Type   string `json:"type"`
		Device string `json:"device"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 1. 获取 token
	body, err := httpGetJSON(tokenAPI, nil, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取二维码失败: " + err.Error()})
		return
	}
	var token qrTokenResp
	if err := json.Unmarshal(body, &token); err != nil || token.Data.Uid == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析二维码数据失败"})
		return
	}

	// 2. 获取二维码图片
	imgBody, err := httpGetJSON(qrcodeImgAPI, url.Values{"uid": {token.Data.Uid}}, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取二维码图片失败: " + err.Error()})
		return
	}
	qrcodeDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBody)

	// 3. 保存会话（记住设备，供轮询时获取 cookie）
	app := mapDeviceToApp(req.Device)
	qrMu.Lock()
	qrSessions[token.Data.Uid] = qrSession{app: app, device: req.Device}
	qrMu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"qrcode": qrcodeDataURI,
		"uid":    token.Data.Uid,
		"time":   token.Data.Time,
		"sign":   token.Data.Sign,
	})
}

// QrCodeStatus 查询扫码状态；成功后自动获取 Cookie 并保存
// POST /storage/qrcode/status  body: { "uid": "", "time": 0, "sign": "" }
func (h *Handler) QrCodeStatus(c *gin.Context) {
	var req struct {
		Uid  string `json:"uid"`
		Time int64  `json:"time"`
		Sign string `json:"sign"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Uid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	query := url.Values{
		"uid":  {req.Uid},
		"time": {fmt.Sprint(req.Time)},
		"sign": {req.Sign},
	}
	body, err := httpGetJSON(statusAPI, query, 60*time.Second)
	if err != nil {
		// 115 状态接口为长轮询，超时说明仍在等待扫码
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
		return
	}
	var st qrStatusResp
	if err := json.Unmarshal(body, &st); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析状态失败"})
		return
	}

	switch st.Data.Status {
	case 0:
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
	case 1:
		c.JSON(http.StatusOK, gin.H{"status": "scanned"})
	case -1:
		h.dropQrSession(req.Uid)
		c.JSON(http.StatusOK, gin.H{"status": "expired"})
	case -2:
		h.dropQrSession(req.Uid)
		c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
	case 2:
		// 已确认，获取 Cookie 并保存
		log.Printf("[115] 二维码已确认登录, uid=%s", req.Uid)
		cookie, username, err := h.fetchAndSaveCookie(req.Uid)
		if err != nil {
			log.Printf("[115] 获取 Cookie 失败: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "username": username, "cookie": cookie})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
	}
}

// fetchAndSaveCookie 调用 login/qrcode 获取 Cookie 并写入 Storage 表
func (h *Handler) fetchAndSaveCookie(uid string) (string, string, error) {
	qrMu.RLock()
	sess, ok := qrSessions[uid]
	qrMu.RUnlock()
	if !ok {
		return "", "", fmt.Errorf("二维码会话已失效，请重新获取")
	}

	api := fmt.Sprintf(resultAPI, sess.app)
	form := url.Values{"app": {sess.app}, "account": {uid}}
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	// 登录换 Cookie 的 UA 必须与后续所有 webapi 请求一致（会话与 UA 绑定）
	req.Header.Set("User-Agent", ua115Unified())
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	var result qrResultResp
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[115] 解析登录结果失败: %v, 响应: %s", err, string(body))
		return "", "", fmt.Errorf("解析登录结果失败")
	}
	if len(result.Data.Cookie) == 0 {
		log.Printf("[115] 登录结果未包含 Cookie, 响应: %s", string(body))
		return "", "", fmt.Errorf("登录成功但未获取到 Cookie")
	}

	// 拼 Cookie 字符串：UID=...; CID=...; SEID=...; KID=...
	username := result.Data.UserName
	parts := make([]string, 0, len(result.Data.Cookie))
	for k, v := range result.Data.Cookie {
		if k == "" {
			continue
		}
		sv := fmt.Sprint(v)
		if sv != "" && sv != "<nil>" {
			parts = append(parts, k+"="+sv)
		}
	}
	// 按 AList/115driver 标准只保留 UID;CID;SEID;KID 四个字段
	// 参考 SheltonZhu/115driver: fmt.Sprintf("UID=%s;CID=%s;SEID=%s;KID=%s", ...)
	cookieMap := map[string]string{}
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 && kv[1] != "" {
			cookieMap[kv[0]] = kv[1]
		}
	}
	cookieKeys := []string{"UID", "CID", "SEID", "KID"}
	cookieParts := make([]string, 0, 4)
	for _, k := range cookieKeys {
		if v, ok := cookieMap[k]; ok && v != "" {
			cookieParts = append(cookieParts, k+"="+v)
		}
	}
	cookie := strings.Join(cookieParts, "; ")

	log.Printf("[115] 扫码登录成功，Cookie长度=%d 字段数=%d, 账号=%s, 统一UA=%s", len(cookie), len(cookieParts), username, ua115Unified())

	// 写入 Cookie 到文件 + 保存设备类型 + 更新 Storage 表元数据
	h.Config.SaveCookie(cookie)
	h.Config.Save115Device(sess.device)
	h.upsert115Storage(cookie, sess.device, username)

	h.dropQrSession(uid)
	return cookie, username, nil
}

// upsert115Storage 保存/更新 115 账号配置
func (h *Handler) upsert115Storage(cookie, device, username string) {
	var storage model.Storage
	err := h.DB.Where("type = ?", "115").First(&storage).Error
	if err != nil {
		// 不存在则创建
		storage = model.Storage{
			Name:   username,
			Type:   "115",
			Cookie: cookie,
			Device: device,
			Status: "online",
		}
		if storage.Name == "" {
			storage.Name = "115主号"
		}
		h.DB.Create(&storage)
		return
	}
	// 存在则更新
	updates := map[string]interface{}{
		"cookie": cookie,
		"device": device,
		"status": "online",
	}
	if username != "" {
		updates["name"] = username
	}
	h.DB.Model(&storage).Updates(updates)
}

// dropQrSession 删除二维码会话
func (h *Handler) dropQrSession(uid string) {
	qrMu.Lock()
	delete(qrSessions, uid)
	qrMu.Unlock()
}
