package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strmhub/internal/model"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 115 同步引擎（webapi） ====================
//
// 数据通道（走 webapi + Cookie，不依赖已暂停服务的开放平台）：
//   - 全量：webapi.115.com/files 递归遍历目录
//   - 增量：life_behavior_detail_app 拉取生活事件
//
// 文件条目字段（webapi files 接口返回）：
//   f = "0" 文件夹 / "1" 文件；n 文件名；fid 文件id；cid 目录id；s 文件大小

const (
	webapi115   = "https://webapi.115.com"
	fileListAPI = "https://webapi.115.com/files/"
	// 生活事件（App 接口，Cookie 认证）：type 省略则逆序拉全部类型；limit 最大 1000
	behaviorAPI = "https://proapi.115.com/android/behavior/detail"
	pageSize    = 1150
)

// webapiFileOrigins 文件列表接口的镜像域名轮换池（p115client get_webapi_origin 同款）
// 单域名被风控（开小差）时切换下一个镜像即可继续使用
var webapiFileOrigins = []string{
	"https://webapi.115.com",
	"http://web.api.115.com",
	"https://115cdn.com/webapi",
	"https://115vod.com/webapi",
}

// remoteFile 115 网盘上的一个文件
type remoteFile struct {
	Fid      string `json:"fid"`
	Name     string `json:"name"`
	Path     string `json:"path"`     // 相对媒体库根目录的路径
	Size     int64  `json:"size"`
	PickCode string `json:"pickcode"` // 下载直链用
	Sha1     string `json:"sha1"`     // 文件 sha1（整理去重用）
	IsAsset  bool   `json:"is_asset"` // 附属文件（图片/字幕/nfo 等，需实体落盘）
}

// syncFilter 全量同步的文件分类过滤器
type syncFilter struct {
	videoExts map[string]bool // 视频：生成 .strm
	assetExts map[string]bool // 附属（图片/字幕/元数据）：下载为真实文件
}

// httpGet115 带 Cookie 的 GET 请求（使用默认 UA）
func httpGet115(api string, query url.Values, cookie string, timeout time.Duration) ([]byte, error) {
	return httpGet115UA(api, query, cookie, "", timeout)
}

// httpGet115UA 带 Cookie 和自定义 UA 的 GET 请求
// UA 必须和扫码登录时的设备类型匹配，否则 115 返回"服务器开小差了"
func httpGet115UA(api string, query url.Values, cookie, ua string, timeout time.Duration) ([]byte, error) {
	return httpGet115Full(api, query, cookie, ua, timeout, nil)
}

// httpGet115Full 完整版请求：可自定义 UA 和额外请求头
func httpGet115Full(api string, query url.Values, cookie, ua string, timeout time.Duration, extraHeaders map[string]string) ([]byte, error) {
	throttle115(api) // 全局节流，防止触发 115 风控
	if ua == "" {
		ua = ua115Unified()
	}
	full := api
	if len(query) > 0 {
		full = api + "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	// UA + Cookie + Referer（浏览器同源请求特征；openStrm 同款）
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", "https://115.com/")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	throttle115Done(api) // 节流锚点推进到本请求完成时刻
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// fetch115FilesPage 从 webapi 拉取一页文件列表（文件+文件夹）
// 主域名被风控返回"开小差"时自动切换镜像域名（p115client get_webapi_origin 轮换同款）
// 返回条目列表、总数、命中的域名
func fetch115FilesPage(cookie, ua, cid string, offset int) ([]map[string]interface{}, int, string, error) {
	query := build115FileQuery(cid, offset)
	var lastErr error
	for _, origin := range webapiFileOrigins {
		body, err := httpGet115UA(origin+"/files", query, cookie, ua, 20*time.Second)
		if err != nil {
			lastErr = err
			log.Printf("[诊断] %s 请求失败: %v", origin, err)
			continue
		}
		var result struct {
			State bool                      `json:"state"`
			Error string                    `json:"error"`
			Data  []map[string]interface{} `json:"data"`
			Count int                       `json:"count"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("解析 115 目录失败: %v", err)
			log.Printf("[诊断] %s 响应无法解析: %s", origin, truncateStr(string(body), 200))
			continue
		}
		if !result.State {
			lastErr = fmt.Errorf("115 返回错误: %s", result.Error)
			log.Printf("[诊断] %s 拒绝: %s", origin, result.Error)
			// Cookie 失效类错误换镜像也没用，直接返回
			if strings.Contains(result.Error, "登录") || strings.Contains(result.Error, "acc") {
				return nil, 0, "", lastErr
			}
			continue // 风控类错误，尝试下一个镜像
		}
		// 成功日志仅在走了镜像回退时记录（主域名成功是常态，不刷屏）
		if origin != webapiFileOrigins[0] {
			log.Printf("[诊断] %s 镜像接管: count=%d, 条目数=%d", origin, result.Count, len(result.Data))
		}
		// state=true 但 count>0 而 data 为空：响应异常，换镜像
		if result.Count > 0 && len(result.Data) == 0 {
			lastErr = fmt.Errorf("响应异常：count=%d 但未返回数据", result.Count)
			continue
		}
		for _, e := range result.Data {
			normalize115Entry(e)
		}
		return result.Data, result.Count, origin, nil
	}
	return nil, 0, "", lastErr
}

// normalize115Entry 归一化 webapi / open 两种条目方言，统一为 webapi 形态：
//   - webapi: f=="0" 目录（cid=目录自身 id）、n=名称、fid=文件 id
//   - open:   fc/file_category==0 目录（fid=自身 id）、fn/file_name=名称
func normalize115Entry(m map[string]interface{}) {
	if _, ok := m["f"]; !ok {
		dir := false
		if v, ok := m["fc"]; ok && fmt.Sprint(v) == "0" {
			dir = true
		}
		if v, ok := m["file_category"]; ok && fmt.Sprint(v) == "0" {
			dir = true
		}
		if dir {
			m["f"] = "0"
		} else {
			m["f"] = "1"
		}
	}
	if fmt.Sprint(m["f"]) == "0" && (fmt.Sprint(m["cid"]) == "" || fmt.Sprint(m["cid"]) == "<nil>") {
		m["cid"] = m["fid"]
	}
	if fmt.Sprint(m["n"]) == "" || fmt.Sprint(m["n"]) == "<nil>" {
		m["n"] = m["fn"]
	}
}

// list115Entries 拉取指定目录的一页条目（文件 + 文件夹），返回条目列表和总数
func list115Entries(cookie, cid string, offset int) ([]map[string]interface{}, int, error) {
	entries, count, _, err := fetch115FilesPage(cookie, ua115Unified(), cid, offset)
	return entries, count, err
}

// walk115Dir 递归遍历目录，按过滤器分别收集视频（生成 strm）和附属文件（实体落盘）
// assets 为 nil 时只收集视频（整理管线等场景）
func walk115Dir(ops *pan115Ops, cid, basePath string, videos, assets *[]remoteFile, f *syncFilter) error {
	dirLabel := basePath
	if dirLabel == "" {
		dirLabel = "(根目录)"
	}
	offset := 0
	for {
		entries, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return err
		}
		// 同步与等待分行显示（等待为该次列表请求因 API 间隔的实际睡眠）
		log.Printf("[同步] 同步%s", dirLabel)
		if w := throttle115LastWait(); w > 0 {
			log.Printf("[同步]   ⏳ API 等待 %.1f 秒", w.Seconds())
		}
		for _, d := range entries {
			isDir := fmt.Sprint(d["f"]) == "0"
			name := fmt.Sprint(d["n"])
			if isDir {
				subPath := path.Join(basePath, name)
				if err := walk115Dir(ops, fmt.Sprint(d["cid"]), subPath, videos, assets, f); err != nil {
					return err
				}
			} else {
				ext := strings.ToLower(path.Ext(name))
				size := int64(0)
				if s, ok := d["s"].(float64); ok {
					size = int64(s)
				}
				sha1 := fmt.Sprint(d["sha"])
				if sha1 == "<nil>" {
					sha1 = ""
				}
				rf := remoteFile{
					Fid:      fmt.Sprint(d["fid"]),
					Name:     name,
					Path:     basePath,
					Size:     size,
					PickCode: fmt.Sprint(d["pc"]),
					Sha1:     sha1,
				}
				switch {
				case len(f.videoExts) > 0 && f.videoExts[ext]:
					if videos != nil {
						*videos = append(*videos, rf)
					}
				case len(f.assetExts) > 0 && f.assetExts[ext]:
					rf.IsAsset = true
					if assets != nil {
						*assets = append(*assets, rf)
					}
				}
			}
		}
		if len(entries) == 0 || offset+len(entries) >= count {
			break
		}
		offset += len(entries)
	}
	return nil
}

// downloadAssetBytes 下载附属文件内容，按 UA/Cookie 组合重试
// UA 必须与直链签发时一致（headers["User-Agent"]），直链对该 UA 绑定
func downloadAssetBytes(rawURL string, cdnHeaders map[string]string, loginCookie string) ([]byte, error) {
	dlUA := cdnHeaders["User-Agent"]
	if dlUA == "" {
		dlUA = ua115Unified()
	}
	setCookie := cdnHeaders["Cookie"]
	combined := setCookie
	if combined != "" && loginCookie != "" {
		combined += "; "
	}
	combined += loginCookie
	type attempt struct{ ua, cookie string }
	var attempts []attempt
	if setCookie != "" {
		attempts = append(attempts, attempt{dlUA, setCookie}) // 同 UA + 直链下发 Cookie（f=3）
	}
	attempts = append(attempts,
		attempt{dlUA, ""},          // openStrm 实战组合：同 UA，不带 Cookie
		attempt{dlUA, combined},    // 直链 Cookie + 登录 Cookie
		attempt{dlUA, loginCookie},
	)
	var lastErr error
	for _, a := range attempts {
		data, err := httpDownloadUA(rawURL, a.ua, a.cookie)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "403") {
			return nil, err // 非 403（如 404/超时）重试无意义
		}
	}
	return nil, fmt.Errorf("CDN 下载被拒(403)：已尝试 UA/Cookie 全部组合，%v", lastErr)
}

// httpDownloadUA 下载文件内容到内存（附属文件均为小文件），UA 可为空串（发空 UA 头）
func httpDownloadUA(rawURL, ua, cookieHeader string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ==================== 115 文件操作（创建目录 / 移动文件） ====================

// httpPostForm115 带 Cookie 的 POST 表单请求
func httpPostForm115(api string, form url.Values, cookie string, timeout time.Duration) ([]byte, error) {
	throttle115(api) // 全局节流，防止触发 115 风控
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua115Unified())
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	throttle115Done(api) // 节流锚点推进到本请求完成时刻
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// mkdir115 在指定父目录下创建文件夹，返回新文件夹 cid
func mkdir115(cookie, parentCid, folderName string) (string, error) {
	// 字段名以 p115client fs_mkdir 为准：pid + cname（旧字段 n 已失效，
	// 发 n 会被 115 判定为"目录名称不能为空"）
	form := url.Values{
		"aid":   {"1"},
		"pid":   {parentCid},
		"cname": {folderName},
	}
	body, err := httpPostForm115("https://webapi.115.com/files/add", form, cookie, 15*time.Second)
	if err != nil {
		return "", err
	}
	// 实测成功响应为平铺结构：{"state":true,"cid":"...","file_id":"...","file_name":"..."}
	// 且 errno 可能是空字符串（不能声明为 int），失败时才有 data/errMsg
	var result struct {
		State   bool            `json:"state"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
		ErrNo   json.RawMessage `json:"errNo"`
		ErrMsg  string          `json:"errMsg"`
		Cid     string          `json:"cid"`
		FileID  string          `json:"file_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析创建目录响应失败: %s", truncateStr(string(body), 150))
	}
	if !result.State {
		if result.ErrMsg != "" {
			return "", fmt.Errorf("创建目录失败: %s", result.ErrMsg)
		}
		return "", fmt.Errorf("创建目录失败: %s", result.Error)
	}
	// 平铺字段优先
	if result.Cid != "" || result.FileID != "" {
		if result.Cid != "" {
			return result.Cid, nil
		}
		return result.FileID, nil
	}
	// 兼容嵌套 data 结构
	var d struct {
		Cid    string `json:"cid"`
		FID    string `json:"fid"`
		FileID string `json:"file_id"`
	}
	if json.Unmarshal(result.Data, &d) == nil {
		if d.Cid != "" {
			return d.Cid, nil
		}
		if d.FID != "" {
			return d.FID, nil
		}
		if d.FileID != "" {
			return d.FileID, nil
		}
	}
	// data 无 cid（如字符串提示）：目录实际已创建，列出父目录按名找回 cid
	if cid, err := findSubDir115(cookie, parentCid, folderName); err == nil && cid != "" {
		return cid, nil
	}
	return "", fmt.Errorf("目录已创建但未获取到 cid（data=%s）", truncateStr(string(result.Data), 100))
}

// findSubDir115 在指定目录下查找名为 name 的子目录，返回 cid（不存在返回空）
func findSubDir115(cookie, parentCid, name string) (string, error) {
	entries, count, err := list115Entries(cookie, parentCid, 0)
	if err != nil {
		return "", err
	}
	for _, d := range entries {
		if fmt.Sprint(d["f"]) == "0" && fmt.Sprint(d["n"]) == name {
			return fmt.Sprint(d["cid"]), nil
		}
	}
	_ = count
	return "", nil
}

// ensure115Path 在 parentCid 下逐级创建目录路径（如 "电影/华语电影/A"），返回最终目录 cid
func ensure115Path(cookie, parentCid, dirPath string) (string, error) {
	parts := strings.Split(dirPath, "/")
	currentCid := parentCid
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 先查找是否已存在
		existingCid, err := findSubDir115(cookie, currentCid, part)
		if err != nil {
			return "", err
		}
		if existingCid != "" {
			currentCid = existingCid
			continue
		}
		// 不存在则创建
		newCid, err := mkdir115(cookie, currentCid, part)
		if err != nil {
			return "", err
		}
		currentCid = newCid
	}
	return currentCid, nil
}

// move115Files 将文件/文件夹移动到目标目录
func move115Files(cookie, targetCid string, fileIds []string) error {
	if len(fileIds) == 0 {
		return nil
	}
	// 字段格式以 p115client fs_move 为准：fid[0]、fid[1]...（重复的裸 fid 已失效）
	form := url.Values{
		"pid": {targetCid},
	}
	for i, fid := range fileIds {
		form.Set(fmt.Sprintf("fid[%d]", i), fid)
	}
	body, err := httpPostForm115("https://webapi.115.com/files/move", form, cookie, 20*time.Second)
	if err != nil {
		return err
	}
	var result struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("解析移动文件响应失败")
	}
	if !result.State {
		msg := result.Error
		if msg == "" {
			msg = "未知错误"
		}
		return fmt.Errorf("移动文件失败: %s", msg)
	}
	return nil
}
func (h *Handler) get115Cookie() (string, error) {
	// 优先从文件读取
	cookie, err := h.Config.LoadCookie()
	if err == nil && cookie != "" {
		return cookie, nil
	}
	// 回退到数据库（兼容旧数据）
	var storage model.Storage
	if err := h.DB.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Cookie == "" {
		return "", fmt.Errorf("尚未绑定 115 账号，请先在「115账号」页扫码登录")
	}
	return storage.Cookie, nil
}

// get115UA 返回与登录设备匹配的 User-Agent
func (h *Handler) get115UA() string {
	device := h.Config.Load115Device()
	if device == "" {
		// 回退到数据库
		var storage model.Storage
		if err := h.DB.Where("type = ?", "115").First(&storage).Error; err == nil && storage.Device != "" {
			device = storage.Device
		}
	}
	return deviceToUA(device)
}

// buildExtSet 把后缀列表（如 ["mp4","mkv"]）转成 map[".mp4"]=true
func buildExtSet(exts []string) map[string]bool {
	set := make(map[string]bool, len(exts))
	for _, e := range exts {
		e = strings.TrimSpace(strings.ToLower(e))
		e = strings.TrimPrefix(e, ".")
		if e != "" {
			set["."+e] = true
		}
	}
	return set
}

// ==================== 生活事件增量 ====================
//
// 接口：P115Client.life_behavior_detail_app
//   GET https://proapi.115.com/{app}/behavior/detail  （app 默认 android，Cookie 认证）
//   参数：type（省略=全部）、limit（≤1000）、offset、date（可选 'YYYY-MM-DD'）
//   注意：有风控风险，两次调用间隔 ≥5 秒；缺少「回收站还原」事件

// 关键操作类型（type 字段，数字或字符串均可能返回）
const (
	evUploadImage = "upload_image_file" // 1 上传图片
	evUpload      = "upload_file"       // 2 上传文件/目录
	evMove        = "move_file"         // 6 移动文件/目录
	evReceive     = "receive_files"     // 14 接收文件（转存）
	evNewFolder   = "new_folder"        // 17 新增目录
	evCopyFolder  = "copy_folder"       // 18 复制目录（目录转存/复制，按目录新增处理）
	evFolderRename = "folder_rename"    // 20 目录改名
	evMoveImage   = "move_image_file"   // 5 移动图片（同移动处理）
	evDelete      = "delete_file"       // 22 删除文件/目录
	evCopy        = "copy_file"         // 23 复制文件
	evRename      = "file_rename"       // 24 文件改名
)

// lifeEvent 一条生活事件
type lifeEvent struct {
	ID       string `json:"id"`        // 115 事件 id（单调递增，增量游标/去重用）
	Type     string `json:"type"`      // 归一化后的操作类型
	FileID   string `json:"file_id"`   // 文件 id
	FileName string `json:"file_name"` // 文件名
	Cid      string `json:"cid"`       // 父目录 cid
	Size     int64  `json:"size"`      // 文件大小
	Time     string `json:"time"`      // 发生时间
}

// normalizeEventType 把数字或字符串类型归一化为字符串
func normalizeEventType(v string) string {
	switch v {
	case "1", "upload_image_file":
		return evUploadImage
	case "2", "upload_file":
		return evUpload
	case "6", "move_file":
		return evMove
	case "14", "receive_files":
		return evReceive
	case "17", "new_folder":
		return evNewFolder
	case "18", "copy_folder":
		return evCopyFolder
	case "5", "move_image_file":
		return evMoveImage
	case "20", "folder_rename":
		return evFolderRename
	case "22", "delete_file":
		return evDelete
	case "23", "copy_file":
		return evCopy
	case "24", "file_rename":
		return evRename
	default:
		return v
	}
}

// firstStr 从多个候选字段名取第一个非空值
func firstStr(d map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v := fmt.Sprint(d[k]); v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

// fetch115LifeEvents 拉取 115 生活事件（type 为空则拉全部类型）
// 响应结构：{state, data: {count, list: [...]}}，事件字段 type/file_id/file_name/cid/file_size/update_time
func fetch115LifeEvents(cookie string, limit, offset int, typ string) ([]lifeEvent, error) {
	query := url.Values{
		"limit":  {fmt.Sprint(limit)},
		"offset": {fmt.Sprint(offset)},
	}
	if typ != "" {
		query.Set("type", typ)
	}
	body, err := httpGet115(behaviorAPI, query, cookie, 20*time.Second)
	if err != nil {
		return nil, err
	}
	var result struct {
		State bool `json:"state"`
		Error string `json:"error"`
		Data  struct {
			// 注意：count 在响应中是字符串（"118262"），声明为 int 会导致整体解析失败；
			// 当前不需要该值，故意不解析
			List []map[string]interface{} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析生活事件失败: %s", truncateStr(string(body), 150))
	}
	if !result.State {
		msg := result.Error
		if msg == "" {
			msg = "state=false"
		}
		return nil, fmt.Errorf("拉取生活事件被拒: %s", msg)
	}
	events := make([]lifeEvent, 0, len(result.Data.List))
	for _, d := range result.Data.List {
		ev := lifeEvent{
			ID:       firstStr(d, "id"),
			Type:     normalizeEventType(fmt.Sprint(d["type"])),
			FileID:   firstStr(d, "file_id", "fid"),
			FileName: firstStr(d, "file_name", "n", "name"),
			Cid:      firstStr(d, "cid", "pid", "parent_id"),
			Time:     firstStr(d, "update_time", "time", "create_time"),
		}
		if s, ok := d["file_size"].(float64); ok {
			ev.Size = int64(s)
		}
		events = append(events, ev)
	}
	return events, nil
}

// get115DirInfo 查询目录自身的 cid/pid/名称（webapi files/get_info，data 为数组取首项）
type dirInfo struct {
	cid, pid, n string
}

// get115DirInfo 查询目录自身的 cid/pid/名称
func get115DirInfo(cookie, cid string) (dirInfo, error) {
	body, err := httpGet115UA("https://webapi.115.com/files/get_info",
		url.Values{"file_id": {cid}}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return dirInfo{}, err
	}
	var r struct {
		State bool `json:"state"`
		Data  []struct {
			Cid string `json:"cid"`
			Pid string `json:"pid"`
			N   string `json:"n"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || !r.State || len(r.Data) == 0 {
		return dirInfo{}, fmt.Errorf("获取目录信息失败: %s", truncateStr(string(body), 120))
	}
	d := r.Data[0]
	return dirInfo{cid: d.Cid, pid: d.Pid, n: d.N}, nil
}

// get115RelPath 从 cid 逐级向上爬父目录链至 rootCid，返回相对路径（如 电影/香港动画/xxx）
// 爬到网盘根仍未遇到 rootCid 说明不在媒体库内，返回 ok=false；memo 缓存减少重复查询
func get115RelPath(cookie, cid, rootCid string, memo map[string]dirInfo) (string, bool, error) {
	if cid == rootCid {
		return "", true, nil
	}
	var parts []string
	cur := cid
	for i := 0; i < 64; i++ { // 深度保险
		info, ok := memo[cur]
		if !ok {
			var err error
			info, err = get115DirInfo(cookie, cur)
			if err != nil {
				return "", false, err
			}
			memo[cur] = info
		}
		if cur == rootCid {
			// 逆序拼接：parts 是从受影响目录向上收集的
			for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
				parts[l], parts[r] = parts[r], parts[l]
			}
			return path.Join(parts...), true, nil
		}
		parts = append(parts, info.n)
		if info.pid == "" || info.pid == "0" {
			return "", false, nil // 到达网盘根，不在媒体库内
		}
		if info.pid == rootCid {
			for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
				parts[l], parts[r] = parts[r], parts[l]
			}
			return path.Join(parts...), true, nil
		}
		cur = info.pid
	}
	return "", false, nil
}

// incrParams 增量同步参数（HTTP 与 cron 调度器共用）
type incrParams struct {
	Cid        string
	LocalPath  string
	VideoExt   []string
	ImageExt   []string
	DataExt    []string
	Limit      int
}

// incrSummary 增量同步结果摘要
type incrSummary struct {
	EventsTotal      int `json:"events_total"`
	EventsFresh      int `json:"events_fresh"`
	Relevant         int `json:"relevant"`
	Structural       int `json:"structural"`
	Deleted          int `json:"deleted"`
	Moved            int `json:"moved"`
	Dirs             int `json:"dirs"`
	DirsSkipped      int `json:"dirs_skipped"`
	Videos           int `json:"videos"`
	StrmCreated      int `json:"strm_created"`
	AssetsTotal      int `json:"assets_total"`
	AssetsDownloaded int `json:"assets_downloaded"`
	AssetsSkipped    int `json:"assets_skipped"`
	AssetsFailed     int `json:"assets_failed"`
	Ignored          int `json:"ignored"` // 非媒体库区域（待整理/已存在/冗余等）的事件
	Elapsed          string `json:"elapsed"`
}

// RunIncrementalSync 增量同步 HTTP 入口
// POST /sync/incremental  body: {"cid":"...","local_path":"...","video_ext":[],"image_ext":[],"data_ext":[],"limit":1000}
func (h *Handler) RunIncrementalSync(c *gin.Context) {
	var req struct {
		Cid        string   `json:"cid"`
		LocalPath  string   `json:"local_path"`
		VideoExt   []string `json:"video_ext"`
		ImageExt   []string `json:"image_ext"`
		DataExt    []string `json:"data_ext"`
		Limit      int      `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	p := normalizeIncrParams(req.Cid, req.LocalPath, req.VideoExt, req.ImageExt, req.DataExt, req.Limit)

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中，请等待完成后再试"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("增量同步")
	defer endTask()

	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "增量同步完成", "summary": sum})
}

func normalizeIncrParams(cid, localPath string, videoExt, imageExt, dataExt []string, limit int) incrParams {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if localPath == "" {
		localPath = "/media"
	}
	if cid == "" {
		cid = "0"
	}
	return incrParams{Cid: cid, LocalPath: localPath, VideoExt: videoExt, ImageExt: imageExt, DataExt: dataExt, Limit: limit}
}

// executeIncrementalSync 增量同步核心（CMS 两阶段模式）：
// 阶段一：小批量分页拉取生活事件并落库去重（SyncEvent 表，事件 id 唯一，永不丢失）
// 阶段二：按时间正序应用事件——新增类定向重遍历受影响目录；
//         move/rename/delete 基于本地文件台账（SyncedFile）精确执行
func (h *Handler) executeIncrementalSync(p incrParams) (*incrSummary, error) {
	sum := &incrSummary{}
	cookie, err := h.get115Cookie()
	if err != nil {
		return sum, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return sum, err
	}

	incrStart := time.Now()
	// 沉淀延迟：等上游转存/移动操作完成，避免拿到中间状态（CMS 同款）
	log.Printf("[同步] ▶ 增量同步开始，等待 3 秒后拉取事件...%s，沉淀等待 3 秒后拉取生活事件...", p.Cid)
	time.Sleep(3 * time.Second)

	// ---- 阶段一：小批量分页拉取，落库去重，直到追平（本页无新事件）----
	// 拉取失败重试：30 秒 × 3 次（网络抖动/瞬时风控不应让整轮作废，QMediaSync 同款）
	fetchWithRetry := func(offset int) ([]lifeEvent, error) {
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			evs, err := fetch115LifeEvents(cookie, 30, offset, "")
			if err == nil {
				return evs, nil
			}
			lastErr = err
			log.Printf("[同步] 事件拉取失败（第 %d/3 次）: %v", attempt, err)
			if attempt < 3 {
				time.Sleep(30 * time.Second)
			}
		}
		return nil, lastErr
	}
	var pending []model.SyncEvent
	offset := 0
	for {
		events, err := fetchWithRetry(offset)
		if err != nil {
			return sum, fmt.Errorf("拉取生活事件失败（已重试 3 次）: %w", err)
		}
		sum.EventsTotal += len(events)
		fresh := 0
		for _, ev := range events {
			if ev.ID == "" {
				continue
			}
			ts, _ := strconv.ParseInt(strings.TrimSpace(ev.Time), 10, 64)
			se := model.SyncEvent{
				EventID: ev.ID, Type: ev.Type, FileID: ev.FileID,
				FileName: ev.FileName, Cid: ev.Cid, Size: ev.Size, EventTime: ts,
			}
			res := h.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&se)
			if res.Error == nil && res.RowsAffected > 0 {
				fresh++
				pending = append(pending, se)
			}
		}
		sum.EventsFresh += fresh
		log.Printf("[同步] 拉取事件: 本页 %d 条，新 %d 条", len(events), fresh)
		if fresh == 0 || sum.EventsFresh >= p.Limit {
			break // 已追平或达到单次上限
		}
		offset += len(events)
		if len(events) < 30 {
			break
		}
	}

	// 事件按时间正序应用（接口返回最新在前）
	for i, j := 0, len(pending)-1; i < j; i, j = i+1, j-1 {
		pending[i], pending[j] = pending[j], pending[i]
	}

	// ---- 阶段二：应用事件 ----
	filter := &syncFilter{
		videoExts: buildExtSet(p.VideoExt),
		assetExts: buildExtSet(append(append([]string{}, p.ImageExt...), p.DataExt...)),
	}
	filter.assetExts[".nfo"] = true
	isMedia := func(name string) bool {
		ext := strings.ToLower(path.Ext(name))
		return filter.videoExts[ext] || filter.assetExts[ext]
	}

	memo := map[string]dirInfo{}
	// 作用域：只监控媒体库子树；待整理/已存在/冗余等整理工作区的事件静默忽略
	libAbs := absPathOf(cookie, p.Cid, memo)
	var excludedAbs []string
	var orgCfgRaw struct {
		Pending   string `json:"pending"`
		Existing  string `json:"existing"`
		Redundant string `json:"redundant"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("org-basic")), &orgCfgRaw)
	for _, cid := range []string{orgCfgRaw.Pending, orgCfgRaw.Existing, orgCfgRaw.Redundant} {
		if cid != "" {
			if a := absPathOf(cookie, cid, memo); a != "" {
				excludedAbs = append(excludedAbs, strings.TrimSuffix(a, "/"))
			}
		}
	}
	scopeOf := func(cid string) string {
		if cid == "" || cid == "0" {
			return "unknown"
		}
		abs := absPathOf(cookie, cid, memo)
		if abs == "" {
			return "unknown"
		}
		abs = strings.TrimSuffix(abs, "/")
		if libAbs != "" && strings.HasPrefix(abs+"/", strings.TrimSuffix(libAbs, "/")+"/") {
			return "library"
		}
		for _, ex := range excludedAbs {
			if strings.HasPrefix(abs+"/", ex+"/") {
				return "excluded"
			}
		}
		return "other"
	}

	dirSet := map[string]bool{}
	for _, ev := range pending {
		switch ev.Type {
		case evUpload, evReceive, evCopy:
			if isMedia(ev.FileName) {
				dirSet[ev.Cid] = true
				sum.Relevant++
			} else if ev.FileID != "" && ev.Cid == "" {
				dirSet[ev.FileID] = true
				sum.Relevant++
			}
		case evNewFolder, evCopyFolder:
			// 目录新增/复制（含整目录转存）：按目录自身加入受影响集合
			if ev.FileID != "" {
				dirSet[ev.FileID] = true
				sum.Relevant++
			}
		case evDelete:
			// 作用域过滤：待整理/已存在/冗余等非媒体库区域的删除不监控
			switch scopeOf(ev.Cid) {
			case "excluded", "other":
				sum.Ignored++
				continue
			case "library":
				// 精确删除：台账 → 路径推导（支持整目录删除与无台账的旧文件）
				if h.removeSyncedItem(ev, cookie, p.Cid, p.LocalPath, memo, false) {
					sum.Deleted++
				}
			default: // unknown（cid=0 等）：仅按台账名称匹配，静默处理
				if h.removeSyncedItem(ev, cookie, p.Cid, p.LocalPath, memo, true) {
					sum.Deleted++
				} else {
					sum.Ignored++
				}
			}
			sum.Structural++
		case evMove, evMoveImage, evRename:
			// 移动/改名：清理旧位置（台账/路径推导），新位置由目录重遍历重建
			if h.removeSyncedItem(ev, cookie, p.Cid, p.LocalPath, memo, true) {
				sum.Moved++
			}
			if ev.Cid != "" && scopeOf(ev.Cid) == "library" {
				dirSet[ev.Cid] = true // 移入媒体库才重建；移入已存在/冗余只清理本地
			}
			sum.Structural++
		case evFolderRename:
			// 目录改名：重遍历父目录重建；旧名子树可能残留，交由后续清理功能
			if ev.Cid != "" {
				dirSet[ev.Cid] = true
			}
			sum.Structural++
		default:
			sum.Structural++
			log.Printf("[同步] ○ 未处理的事件: 类型=%s 文件=%s", ev.Type, ev.FileName)
		}
	}

	// 受影响目录：定位相对路径 + 祖先去重
	type targetDir struct{ cid, base string }
	var targets []targetDir
	for cid := range dirSet {
		base, ok, err := get115RelPath(cookie, cid, p.Cid, memo)
		if err != nil {
			log.Printf("[同步] 定位目录路径失败 cid=%s: %v", cid, err)
			sum.DirsSkipped++
			continue
		}
		if !ok {
			sum.DirsSkipped++ // 不在媒体库路径下
			continue
		}
		targets = append(targets, targetDir{cid: cid, base: base})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].base < targets[j].base })
	var uniqTargets []targetDir
	dirsMerged := 0
	for _, t := range targets {
		covered := false
		for _, u := range uniqTargets {
			if u.base == "" || t.base == u.base || strings.HasPrefix(t.base, u.base+"/") {
				covered = true
				break
			}
		}
		if covered {
			dirsMerged++ // 子目录被上层目录的遍历覆盖，无需单独处理
			continue
		}
		uniqTargets = append(uniqTargets, t)
	}

	// 逐目录遍历并立即落盘
	domain, format, keepExt, skipExist := h.getStrmConfig()
	for _, t := range uniqTargets {
		var videos, assets []remoteFile
		if err := walk115Dir(ops, t.cid, t.base, &videos, &assets, filter); err != nil {
			log.Printf("[同步] 遍历目录失败 %s: %v，30 秒后重试一次", t.base, err)
			time.Sleep(30 * time.Second)
			if err := walk115Dir(ops, t.cid, t.base, &videos, &assets, filter); err != nil {
				log.Printf("[同步] 遍历目录重试仍失败 %s: %v，跳过", t.base, err)
				sum.DirsSkipped++
				continue
			}
		}
		sc, dl, sk, fl := applySyncResults(h.DB, ops, videos, assets, p.LocalPath, domain, format, keepExt, skipExist, t.base)
		sum.Dirs++
		sum.Videos += len(videos)
		sum.StrmCreated += sc
		sum.AssetsTotal += len(assets)
		sum.AssetsDownloaded += dl
		sum.AssetsSkipped += sk
		sum.AssetsFailed += fl
		log.Printf("[同步] %s: 视频 %d（STRM %d），附属 %d（下载 %d，跳过 %d）", t.base, len(videos), sc, len(assets), dl, sk)
	}

	// 标记事件已应用 + 更新水位
	now := time.Now()
	ids := make([]string, 0, len(pending))
	for _, ev := range pending {
		ids = append(ids, ev.EventID)
	}
	if len(ids) > 0 {
		h.DB.Model(&model.SyncEvent{}).Where("event_id IN ?", ids).
			Updates(map[string]interface{}{"status": "applied", "applied_at": now})
	}
	h.Config.SaveSetting("incr-last", fmt.Sprint(now.Unix()))

	if sum.StrmCreated+sum.AssetsDownloaded+sum.Deleted+sum.Moved > 0 {
		h.notifyEmbyRefresh(p.LocalPath)
	}
	sum.Elapsed = time.Since(incrStart).Truncate(time.Second).String()
	log.Printf("[同步] ✅ 增量同步完成（耗时 %s · 拉取 %d 条（新 %d），媒体相关 %d，结构性 %d（删 %d，移/改 %d），非库区忽略 %d，目录命中 %d（处理 %d，合并覆盖 %d，库外跳过 %d），视频 %d（STRM %d），附属下载 %d",
		sum.Elapsed, sum.EventsTotal, sum.EventsFresh, sum.Relevant, sum.Structural, sum.Deleted, sum.Moved, sum.Ignored, len(uniqTargets)+sum.DirsSkipped, sum.Dirs, dirsMerged, sum.DirsSkipped, sum.Videos, sum.StrmCreated, sum.AssetsDownloaded)
	return sum, nil
}

// absPathOf 爬到网盘根，返回目录绝对路径（如 /整理/已存在），失败返回空
func absPathOf(cookie, cid string, memo map[string]dirInfo) string {
	if cid == "" || cid == "0" {
		return ""
	}
	var parts []string
	cur := cid
	for i := 0; i < 64; i++ {
		info, ok := memo[cur]
		if !ok {
			var err error
			info, err = get115DirInfo(cookie, cur)
			if err != nil {
				return ""
			}
			memo[cur] = info
		}
		parts = append(parts, info.n)
		if info.pid == "" || info.pid == "0" {
			break
		}
		cur = info.pid
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return "/" + strings.Join(parts, "/")
}

// removeSyncedFile 按文件 id 从台账定位并删除本地文件（仅删除本工具生成过的文件）
func (h *Handler) removeSyncedFile(fileID, localRoot string) bool {
	if fileID == "" {
		return false
	}
	var sf model.SyncedFile
	if err := h.DB.Where("file_id = ?", fileID).First(&sf).Error; err != nil {
		return false // 台账无记录（从未同步过），无需处理
	}
	full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		log.Printf("[同步] 删除本地文件失败 %s: %v", full, err)
		return false
	}
	h.DB.Delete(&sf)
	log.Printf("[同步] ✓ 本地文件已清理: %s", sf.RelPath)
	return true
}

// removeSyncedItem 清理 move/rename/delete 事件的旧位置，三级定位：
// 1) 台账按 file_id 精确匹配（本工具生成且已登记的文件）
// 2) 路径推导：解析父目录相对路径 + 事件文件名；目标是目录则整树删除
//    （覆盖"删除整个影视目录"及台账启用前同步的历史文件）
// 3) 台账按文件名模糊匹配兜底（父目录已被连带删除导致路径推导失败时）
func (h *Handler) removeSyncedItem(ev model.SyncEvent, cookie, rootCid, localRoot string, memo map[string]dirInfo, quiet bool) bool {
	// 1) 台账精确匹配
	if ev.FileID != "" && h.removeSyncedFile(ev.FileID, localRoot) {
		return true
	}
	// 2) 路径推导
	if ev.Cid != "" && ev.FileName != "" {
		if base, ok, err := get115RelPath(cookie, ev.Cid, rootCid, memo); err == nil && ok {
			rel := path.Join(base, ev.FileName)
			local := filepath.Join(localRoot, filepath.FromSlash(rel))
			// 目录：整树删除（strm/附属全在树内），并清理台账
			if st, err := os.Stat(local); err == nil && st.IsDir() {
				if err := os.RemoveAll(local); err != nil {
					log.Printf("[同步] 删除本地目录失败 %s: %v", rel, err)
					return false
				}
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", rel, rel+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] 目录删除-执行成功: %s", rel)
				return true
			}
			// 文件：strm 与附属实体两种形态
			for _, cand := range []struct{ rel, suffix string }{{rel, ".strm"}, {rel, ""}} {
				full := filepath.Join(localRoot, filepath.FromSlash(cand.rel)) + cand.suffix
				if _, err := os.Stat(full); err == nil {
					if err := os.Remove(full); err != nil {
						log.Printf("[同步] 删除本地文件失败 %s: %v", cand.rel+cand.suffix, err)
						return false
					}
					h.DB.Where("rel_path = ?", cand.rel+cand.suffix).Delete(&model.SyncedFile{})
					log.Printf("[同步] ✓ 本地文件已清理: %s", cand.rel+cand.suffix)
					return true
				}
			}
		}
	}
	// 3) 台账按文件名模糊兜底
	if ev.FileName != "" {
		var sfs []model.SyncedFile
		h.DB.Where("rel_path = ? OR rel_path = ?", ev.FileName+".strm", ev.FileName).Find(&sfs)
		// 进一步按文件名后缀精确过滤（rel_path 最后一段必须完全等于）
		for _, sf := range sfs {
			if path.Base(sf.RelPath) == ev.FileName+".strm" || path.Base(sf.RelPath) == ev.FileName {
				full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
				if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
					continue
				}
				h.DB.Delete(&sf)
				log.Printf("[同步] ✓ 本地文件已清理: %s", sf.RelPath)
				return true
			}
		}
		// 目录事件：台账中出现过该名称路径段的，按最浅前缀整树删除
		var segs []model.SyncedFile
		h.DB.Where("rel_path LIKE ? OR rel_path LIKE ?", "%/"+ev.FileName+"/%", "%/"+ev.FileName).Limit(50).Find(&segs)
		bestPrefix := ""
		for _, sf := range segs {
			parts := strings.Split(sf.RelPath, "/")
			for i, part := range parts {
				if part == ev.FileName {
					prefix := strings.Join(parts[:i+1], "/")
					if bestPrefix == "" || len(prefix) < len(bestPrefix) {
						bestPrefix = prefix
					}
					break
				}
			}
		}
		if bestPrefix != "" {
			full := filepath.Join(localRoot, filepath.FromSlash(bestPrefix))
			if err := os.RemoveAll(full); err == nil {
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", bestPrefix, bestPrefix+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地目录已清理: %s", bestPrefix)
				return true
			}
		}
	}
	// 4) 本地磁盘按名搜索兜底（父目录与台账均不可用时）：
	//    目录精确名匹配取最浅层整树删除；文件匹配 实体/strm 两种形态
	if ev.FileName != "" {
		var hitDir, hitFile string
		filepath.WalkDir(localRoot, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if name == ev.FileName {
				if d.IsDir() {
					if hitDir == "" || len(p) < len(hitDir) {
						hitDir = p
					}
				} else {
					hitFile = p
				}
			} else if name == ev.FileName+".strm" {
				hitFile = p
			}
			return nil
		})
		if hitDir != "" {
			if err := os.RemoveAll(hitDir); err == nil {
				rel, _ := filepath.Rel(localRoot, hitDir)
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", filepath.ToSlash(rel), filepath.ToSlash(rel)+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地目录已清理: %s", rel)
				return true
			}
		}
		if hitFile != "" {
			if err := os.Remove(hitFile); err == nil {
				rel, _ := filepath.Rel(localRoot, hitFile)
				h.DB.Where("rel_path = ?", filepath.ToSlash(rel)).Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地文件已清理: %s", rel)
				return true
			}
		}
	}
	if !quiet {
		log.Printf("[同步] ○ 本地未找到对应文件: %s", ev.FileName)
	}
	return false
}

// ==================== 全量同步 ====================

// fullSyncMu 全量同步互斥：防止重复点击导致两个同步并发互相干扰
var fullSyncMu sync.Mutex

// taskState 当前任务状态（供前端展示与按钮禁用，含 cron 触发的任务）
var (
	taskStateMu sync.Mutex
	taskRunning bool
	taskName    string
	taskStart   time.Time
)

func beginTask(name string) {
	taskStateMu.Lock()
	taskRunning, taskName, taskStart = true, name, time.Now()
	taskStateMu.Unlock()
}

func endTask() {
	taskStateMu.Lock()
	taskRunning = false
	taskStateMu.Unlock()
}

// TaskStatus 当前任务状态快照
func TaskStatus() (bool, string, time.Time) {
	taskStateMu.Lock()
	defer taskStateMu.Unlock()
	return taskRunning, taskName, taskStart
}

// RunFullSync 执行全量同步：递归遍历 cid 目录，视频生成 .strm，附属文件实体落盘
// 附属文件 = 用户配置的图片后缀 + 数据文件后缀 + nfo（Emby/Jellyfin 标准元数据）；
// 不在过滤集合内的文件一律不同步
// POST /sync/full  body: {"cid":"...","local_path":"...","video_ext":["mp4"],"image_ext":["jpg"],"data_ext":["ass"]}
func (h *Handler) RunFullSync(c *gin.Context) {
	var req struct {
		Cid        string   `json:"cid"`
		LocalPath  string   `json:"local_path"`
		VideoExt   []string `json:"video_ext"`
		ImageExt   []string `json:"image_ext"`
		DataExt    []string `json:"data_ext"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Cid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：请填写 115 媒体库 cid"})
		return
	}
	if req.LocalPath == "" {
		req.LocalPath = "/media"
	}

	// 同一时刻只允许一个全量同步
	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中，请等待完成后再试"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("全量同步")
	defer endTask()
	fullStart := time.Now()

	// 读取 STRM 直链配置
	domain, format, keepExt, skipExist := h.getStrmConfig()

	// 构造统一操作通道（OpenAPI 优先，Cookie 回退）
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 过滤器：视频组生成 strm；附属组 = 图片后缀 ∪ 数据后缀 ∪ .nfo（始终包含）
	filter := &syncFilter{
		videoExts: buildExtSet(req.VideoExt),
		assetExts: buildExtSet(append(append([]string{}, req.ImageExt...), req.DataExt...)),
	}
	filter.assetExts[".nfo"] = true

	// 递归遍历，分类收集
	var videos, assets []remoteFile
	if err := walk115Dir(ops, req.Cid, "", &videos, &assets, filter); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "遍历 115 目录失败: " + err.Error()})
		return
	}

	strmCreated, downloaded, skipped, failed := applySyncResults(h.DB, ops, videos, assets, req.LocalPath, domain, format, keepExt, skipExist, "")

	totalNew := strmCreated + downloaded
	if totalNew > 0 {
		h.notifyEmbyRefresh(req.LocalPath)
	}
	// 全量已覆盖一切：把事件窗口内的生活事件标记为已处理，
	// 之后的增量同步只处理此后发生的新事件
	if cookieOnly, err := h.get115Cookie(); err == nil {
		if n, err := h.markEventsCoveredByFullSync(cookieOnly); err != nil {
			log.Printf("[同步] 标记生活事件已覆盖失败: %v", err)
		} else if n > 0 {
			log.Printf("[同步] 生活事件窗口已标记为已覆盖: %d 条（增量同步只处理此后新事件）", n)
		}
	}
	log.Printf("[同步] ✅ 全量同步完成（耗时 %s · 视频 %d（生成 STRM %d），附属文件 %d（下载 %d，跳过 %d，失败 %d）",
		time.Since(fullStart).Truncate(time.Second), len(videos), strmCreated, len(assets), downloaded, skipped, failed)

	c.JSON(http.StatusOK, gin.H{
		"message": "全量同步完成",
		"elapsed": time.Since(fullStart).Truncate(time.Second).String(),
		"total":   len(videos),
		"created": strmCreated,
		"assets_total":      len(assets),
		"assets_downloaded": downloaded,
		"assets_skipped":    skipped,
		"assets_failed":     failed,
	})
}

// assetDLWorkers 附属文件并发下载线程数（CDN 下载不占 API 限额，CMS 同款思路）
const assetDLWorkers = 5

// applySyncResults 对遍历结果执行落盘：视频生成 strm，附属文件下载（已存在跳过），
// 全部登记到 SyncedFile 台账（move/delete 事件精确执行的依据）
func applySyncResults(db *gorm.DB, ops *pan115Ops, videos, assets []remoteFile, localPath, domain, format string, keepExt, skipExist bool, dirLabel string) (strmCreated, downloaded, skipped, failed int) {
	for _, f := range videos {
		if err := writeStrm(localPath, domain, format, keepExt, skipExist, f); err != nil {
			log.Printf("[同步] 生成 STRM 失败: %s/%s: %v", f.Path, f.Name, err)
			continue
		}
		strmCreated++
		upsertSyncedFile(db, f, path.Join(f.Path, f.Name+".strm"), "video")
	}

	// 附属文件：生产者串行取直链（守 API 间隔），worker 池并发下载
	type assetJob struct {
		f    remoteFile
		url  string
		hdrs map[string]string
	}
	type assetRes struct {
		f      remoteFile
		status string
		err    error
	}
	jobs := make(chan assetJob)
	resCh := make(chan assetRes, len(assets))
	var wg sync.WaitGroup
	for i := 0; i < assetDLWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				data, err := downloadAssetBytes(j.url, j.hdrs, ops.cookieForDL())
				if err != nil {
					resCh <- assetRes{f: j.f, err: err}
					continue
				}
				st, err := writeAssetBytes(j.f, localPath, data)
				resCh <- assetRes{f: j.f, status: st, err: err}
			}
		}()
	}
	for i, f := range assets {
		if i%20 == 0 && i > 0 {
			log.Printf("[同步] 附属文件进度: %d/%d", i, len(assets))
		}
		dst := filepath.Join(localPath, filepath.FromSlash(f.Path), f.Name)
		if _, err := os.Stat(dst); err == nil {
			resCh <- assetRes{f: f, status: "skip"}
			upsertSyncedFile(db, f, path.Join(f.Path, f.Name), "asset")
			continue
		}
		u, hdrs, err := ops.downloadURLFull(f.PickCode, "")
		if err != nil {
			resCh <- assetRes{f: f, err: err}
			continue
		}
		jobs <- assetJob{f: f, url: u, hdrs: hdrs}
	}
	close(jobs)
	wg.Wait()
	close(resCh)
	for r := range resCh {
		switch {
		case r.err != nil:
			failed++
			log.Printf("[同步] 附属文件失败: %s/%s: %v", r.f.Path, r.f.Name, r.err)
		case r.status == "skip":
			skipped++
		default:
			downloaded++
			upsertSyncedFile(db, r.f, path.Join(r.f.Path, r.f.Name), "asset")
		}
	}
	return
}

// upsertSyncedFile 登记本地文件台账（file_id 唯一）
func upsertSyncedFile(db *gorm.DB, f remoteFile, relPath, kind string) {
	if db == nil || f.Fid == "" {
		return
	}
	sf := model.SyncedFile{FileID: f.Fid, PickCode: f.PickCode, RelPath: relPath, Kind: kind, Size: f.Size, Sha1: f.Sha1}
	db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pick_code", "rel_path", "kind", "size", "sha1", "updated_at"}),
	}).Create(&sf)
}

// writeAssetBytes 把附属文件内容写到本地（.part 临时文件原子改名）
func writeAssetBytes(f remoteFile, localRoot string, data []byte) (string, error) {
	dir := filepath.Join(localRoot, filepath.FromSlash(f.Path))
	dst := filepath.Join(dir, f.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return "download", nil
}

// getSettingValue 读取配置：yaml 优先，数据库回退（兼容旧数据）
// 前端 saveConfig 保存到 yaml，早期版本保存到 DB，两处都要能读到
func (h *Handler) getSettingValue(key string) string {
	if v := h.Config.GetSetting(key); v != "" {
		return v
	}
	var s model.Setting
	if err := h.DB.Where("key = ?", key).First(&s).Error; err == nil {
		return s.Value
	}
	return ""
}

// markEventsCoveredByFullSync 全量同步完成后调用：
// 把事件窗口内的生活事件直接落库并标记为已处理（全量已覆盖一切，无需增量再处理）
func (h *Handler) markEventsCoveredByFullSync(cookie string) (int, error) {
	count := 0
	offset := 0
	for {
		events, err := fetch115LifeEvents(cookie, 30, offset, "")
		if err != nil {
			return count, err
		}
		fresh := 0
		for _, ev := range events {
			if ev.ID == "" {
				continue
			}
			ts, _ := strconv.ParseInt(strings.TrimSpace(ev.Time), 10, 64)
			now := time.Now()
			se := model.SyncEvent{
				EventID: ev.ID, Type: ev.Type, FileID: ev.FileID,
				FileName: ev.FileName, Cid: ev.Cid, Size: ev.Size,
				EventTime: ts, Status: "applied", AppliedAt: &now,
			}
			res := h.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&se)
			if res.Error == nil && res.RowsAffected > 0 {
				fresh++
				count++
			}
		}
		if fresh == 0 || count >= 1000 {
			break
		}
		offset += len(events)
		if len(events) < 30 {
			break
		}
	}
	return count, nil
}

// rename115 重命名网盘文件（webapi files/batch_rename；字幕随视频新名对齐用）
func rename115(cookie, fid, newName string) error {
	form := url.Values{}
	form.Set("files_new_name["+fid+"]", newName)
	body, err := httpPostForm115("https://webapi.115.com/files/batch_rename", form, cookie, 15*time.Second)
	if err != nil {
		return err
	}
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &r) == nil && !r.State {
		return fmt.Errorf("重命名被拒: %s", r.Error)
	}
	return nil
}

// getStrmConfig 读取 STRM 直链配置
func (h *Handler) getStrmConfig() (domain, format string, keepExt, exist bool) {
	domain = "http://172.17.0.1:6060"
	format = "pick_code_name"
	keepExt = true
	exist = false // false=覆盖
	var s model.Setting
	if err := h.DB.Where("key = ?", "strm").First(&s).Error; err != nil {
		return
	}
	var cfg struct {
		Domain  string `json:"domain"`
		Format  string `json:"format"`
		KeepExt any    `json:"keep_ext"`
		Exist   string `json:"exist"`
	}
	if json.Unmarshal([]byte(s.Value), &cfg) == nil {
		if cfg.Domain != "" {
			domain = cfg.Domain
		}
		if cfg.Format != "" {
			format = cfg.Format
		}
		switch v := cfg.KeepExt.(type) {
		case bool:
			keepExt = v
		case string:
			keepExt = v == "true"
		}
		if cfg.Exist == "skip" {
			exist = true // skip=true 表示跳过已存在
		}
	}
	return
}

// writeStrm 生成单个 .strm 文件
func writeStrm(localRoot, domain, format string, keepExt, skipExist bool, f remoteFile) error {
	var streamURL string
	if format == "pick_code" {
		streamURL = fmt.Sprintf("%s/d/%s", strings.TrimRight(domain, "/"), f.Fid)
	} else {
		if keepExt {
			streamURL = fmt.Sprintf("%s/d/%s/%s", strings.TrimRight(domain, "/"), f.Fid, f.Name)
		} else {
			name := f.Name
			if ext := path.Ext(name); ext != "" {
				name = strings.TrimSuffix(name, ext)
			}
			streamURL = fmt.Sprintf("%s/d/%s/%s", strings.TrimRight(domain, "/"), f.Fid, name)
		}
	}

	// 本地目录：保持网盘目录结构
	dir := filepath.Join(localRoot, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	strmName := f.Name + ".strm"
	strmPath := filepath.Join(dir, strmName)

	// 如果配置为跳过已存在，且文件已存在则跳过
	if skipExist {
		if _, err := os.Stat(strmPath); err == nil {
			return nil
		}
	}

	return os.WriteFile(strmPath, []byte(streamURL), 0o644)
}

// ==================== 整理→同步闭环 ====================

// executeOrganize 整理核心（HTTP 与 cron 调度器共用）：
// 加载配置 → 整理引擎 → 可选对影视库执行全量同步
// 返回步骤摘要与错误（错误时 steps 里带原因）
func (h *Handler) executeOrganize(syncAfter bool) ([]gin.H, []OrganizeResult, error) {
	orgStart := time.Now()
	log.Printf("[整理] ▶ 开始整理 %s", time.Now().Format("15:04:05"))
	steps := []gin.H{}

	orgCfg, err := h.loadOrgConfig()
	if err != nil {
		return append(steps, gin.H{"step": "整理", "status": "跳过", "message": err.Error()}), nil, err
	}

	ops, err := h.newPan115Ops()
	if err != nil {
		return append(steps, gin.H{"step": "整理", "status": "失败", "message": err.Error()}), nil, err
	}

	logFn := func(msg string) { log.Println(msg) }
	orgResults, successCount := runOrganizeEngine(ops, orgCfg, logFn)

	totalFiles := len(orgResults)
	existsCount := 0
	failedCount := 0
	for _, r := range orgResults {
		if r.Status == "exists" {
			existsCount++
		}
		if r.Status == "failed" {
			failedCount++
		}
	}

	steps = append(steps, gin.H{"step": "整理", "status": "完成", "message": fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", totalFiles, successCount, existsCount, failedCount)})
	strmTotal, strmCreated := 0, 0

	// 整理后对影视库执行全量同步生成 STRM
	if syncAfter {
		var syncCfg struct {
			LocalPath string   `json:"local_path"`
			VideoExt  []string `json:"video_ext"`
			ImageExt  []string `json:"image_ext"`
			DataExt   []string `json:"data_ext"`
		}
		if v := h.getSettingValue("full"); v != "" {
			json.Unmarshal([]byte(v), &syncCfg)
		}
		if syncCfg.LocalPath == "" {
			syncCfg.LocalPath = "/media"
		}

		domain, format, keepExt, skipExist := h.getStrmConfig()
		filter := &syncFilter{
			videoExts: buildExtSet([]string{"mp4", "mkv", "ts", "avi", "mov", "rmvb", "webm", "flv", "m2ts", "wmv", "mpg", "iso"}),
			assetExts: map[string]bool{},
		}
		if len(syncCfg.VideoExt) > 0 {
			filter.videoExts = buildExtSet(syncCfg.VideoExt)
		}
		var videos, assets []remoteFile
		if err := walk115Dir(ops, orgCfg.Library, "", &videos, &assets, filter); err != nil {
			steps = append(steps, gin.H{"step": "STRM 同步", "status": "失败", "message": "遍历目录失败: " + err.Error()})
			return steps, orgResults, nil
		}
		sc, dl, _, _ := applySyncResults(h.DB, ops, videos, assets, syncCfg.LocalPath, domain, format, keepExt, skipExist, "")
		strmTotal, strmCreated = len(videos), sc
		steps = append(steps, gin.H{"step": "STRM 同步", "status": "完成", "message": fmt.Sprintf("共 %d 个视频，生成 %d 个 STRM，附属下载 %d", len(videos), sc, dl)})
		if sc+dl > 0 {
			h.notifyEmbyRefresh(syncCfg.LocalPath)
		}
	}

	// 消息通知
	if successCount > 0 {
		var titles []string
		for _, r := range orgResults {
			if r.Status == "success" {
				line := r.Title
				if r.Year != "" {
					line += " (" + r.Year + ")"
				}
				if r.Category != "" {
					line += " [" + r.Category + "]"
				}
				titles = append(titles, line)
			}
		}
		NotifyMessage(
			fmt.Sprintf("整理完成，新增 %d 部", successCount),
			strings.Join(titles, "\n"),
		)
	}
	// 按部汇总（一部剧的 52 个文件归并为一行）
	showSet := map[string]bool{}
	var showLines []string
	for _, r := range orgResults {
		if r.TmdbID == 0 || r.Status != "success" {
			continue
		}
		key := fmt.Sprintf("%d-%s", r.TmdbID, r.TargetDir)
		if showSet[key] {
			continue
		}
		showSet[key] = true
		line := fmt.Sprintf("%s (%s) → %s", r.Title, r.Year, r.TargetDir)
		showLines = append(showLines, line)
	}
	if len(showLines) > 0 {
		log.Printf("[整理] 本次入库 %d 部:\n  %s", len(showLines), strings.Join(showLines, "\n  "))
	}

	log.Printf("[整理] ✅ 整理完成（耗时 %s · 共 %d 项（成功 %d，已存在 %d，失败 %d），STRM 同步 %s",
		time.Since(orgStart).Truncate(time.Second), totalFiles, successCount, existsCount, failedCount,
		map[bool]string{true: fmt.Sprintf("已执行（%d 视频，生成 %d STRM）", strmTotal, strmCreated), false: "未执行"}[syncAfter])
	_ = strmTotal
	_ = strmCreated
	return steps, orgResults, nil
}

// RunOrganizePipeline 整理→同步闭环 HTTP 入口
// POST /organize/pipeline  body: {"sync_after": true}
func (h *Handler) RunOrganizePipeline(c *gin.Context) {
	var req struct {
		SyncAfter bool `json:"sync_after"`
	}
	c.ShouldBindJSON(&req)

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中，请等待完成后再试"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("自动整理")
	defer endTask()

	steps, details, _ := h.executeOrganize(req.SyncAfter)
	// 按部归并（前端一行一部）
	showSet := map[string]bool{}
	var shows []gin.H
	for _, r := range details {
		if r.TmdbID == 0 || r.Status != "success" {
			continue
		}
		key := fmt.Sprintf("%d-%s", r.TmdbID, r.TargetDir)
		if showSet[key] {
			continue
		}
		showSet[key] = true
		shows = append(shows, gin.H{"title": r.Title, "year": r.Year, "category": r.Category, "target": r.TargetDir})
	}
	c.JSON(http.StatusOK, gin.H{"steps": steps, "details": details, "shows": shows, "message": "整理执行完成"})
}

// ==================== EMBY 入库刷新通知 ====================

// notifyEmbyRefresh STRM 生成后通知 Emby 刷新入库
// 如果配置了路径替换规则，将本地路径转为 Emby 路径后调用 Emby Library scan API
func (h *Handler) notifyEmbyRefresh(localPath string) {
	var s model.Setting
	if err := h.DB.Where("key = ?", "emby-refresh").First(&s).Error; err != nil {
		return
	}
	var cfg struct {
		PathRule string `json:"path_rule"`
		Style    string `json:"style"`
		Enabled  any    `json:"enabled"`
	}
	if json.Unmarshal([]byte(s.Value), &cfg) != nil {
		return
	}
	// 检查是否启用
	enabled := true
	switch v := cfg.Enabled.(type) {
	case bool:
		enabled = v
	case string:
		enabled = v == "true"
	}
	if !enabled {
		return
	}

	// 路径替换
	embyPath := localPath
	if cfg.PathRule != "" && strings.Contains(cfg.PathRule, "#") {
		parts := strings.SplitN(cfg.PathRule, "#", 2)
		src, dst := parts[0], parts[1]
		if src != "" && strings.HasPrefix(localPath, src) {
			embyPath = dst + strings.TrimPrefix(localPath, src)
		}
	}
	if cfg.Style == "windows" {
		embyPath = strings.ReplaceAll(embyPath, "/", "\\")
	}

	// 读取 Emby webhook 配置中的 server 地址（暂用 webhook 地址的 host）
	var embySetting model.Setting
	if h.DB.Where("key = ?", "emby-notify").First(&embySetting).Error != nil {
		return
	}
	var embyCfg struct {
		Webhook string `json:"webhook"`
	}
	json.Unmarshal([]byte(embySetting.Value), &embyCfg)

	// 从 webhook 地址提取 Emby server
	if embyCfg.Webhook == "" {
		return
	}
	// webhook 格式: http://ip:port/api/emby/webhook?token=xxx
	u, err := url.Parse(embyCfg.Webhook)
	if err != nil {
		return
	}
	embyServer := u.Scheme + "://" + u.Host

	client := &http.Client{Timeout: 10 * time.Second}

	// 优先按库刷新（CMS 同款）：取媒体库列表，将变更路径映射到所属库后逐库 Refresh
	apiKey := ""
	var refreshCfg struct {
		ApiKey string `json:"api_key"`
	}
	_ = json.Unmarshal([]byte(s.Value), &refreshCfg)
	apiKey = strings.TrimSpace(refreshCfg.ApiKey)
	q := ""
	if apiKey != "" {
		q = "?api_key=" + url.QueryEscape(apiKey)
	}
	libResp, err := client.Get(embyServer + "/Library/MediaFolders" + q)
	if err == nil {
		defer libResp.Body.Close()
		var libs struct {
			Items []struct {
				ID         string   `json:"Id"`
				Name       string   `json:"Name"`
				Locations  []string `json:"Locations"`
			} `json:"Items"`
		}
		if json.NewDecoder(libResp.Body).Decode(&libs) == nil {
			refreshed := 0
			for _, lib := range libs.Items {
				hit := false
				for _, loc := range lib.Locations {
					if strings.HasPrefix(embyPath, strings.TrimRight(loc, "/")+"/") || embyPath == loc {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
				r, err := client.Post(embyServer+"/Library/"+lib.ID+"/Refresh"+q, "", nil)
				if err != nil {
					log.Printf("Emby 按库刷新失败 %s: %v", lib.Name, err)
					continue
				}
				r.Body.Close()
				refreshed++
				log.Printf("Emby 媒体库刷新任务提交成功：%s %s", lib.ID, lib.Name)
			}
			if refreshed > 0 {
				return
			}
			log.Printf("Emby 按库刷新：变更路径未命中任何媒体库（%s），回退路径通知", embyPath)
		}
	}

	// 回退：按路径通知
	// POST /Library/Media/Updated { "Updates": [{"Path":"...","UpdateType":"Created"}] }
	body, _ := json.Marshal(map[string]interface{}{
		"Updates": []map[string]string{
			{"Path": embyPath, "UpdateType": "Created"},
		},
	})
	req, _ := http.NewRequest("POST", embyServer+"/Library/Media/Updated"+q, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Emby 刷新通知失败: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("Emby 刷新通知已发送: %s", embyPath)
}
