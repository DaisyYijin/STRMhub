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
	"strmhub/internal/model"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
			log.Printf("[115目录] %s 请求失败: %v", origin, err)
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
			log.Printf("[115目录] %s 响应无法解析: %s", origin, truncateStr(string(body), 200))
			continue
		}
		if !result.State {
			lastErr = fmt.Errorf("115 返回错误: %s", result.Error)
			log.Printf("[115目录] %s 拒绝: %s", origin, result.Error)
			// Cookie 失效类错误换镜像也没用，直接返回
			if strings.Contains(result.Error, "登录") || strings.Contains(result.Error, "acc") {
				return nil, 0, "", lastErr
			}
			continue // 风控类错误，尝试下一个镜像
		}
		// 成功日志仅在走了镜像回退时记录（主域名成功是常态，不刷屏）
		if origin != webapiFileOrigins[0] {
			log.Printf("[115目录] %s 镜像接管: count=%d, 条目数=%d", origin, result.Count, len(result.Data))
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
	log.Printf("[115同步] 遍历目录: %s", map[bool]string{true: basePath, false: "(根目录)"}[basePath != ""])
	offset := 0
	for {
		entries, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return err
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
				rf := remoteFile{
					Fid:      fmt.Sprint(d["fid"]),
					Name:     name,
					Path:     basePath,
					Size:     size,
					PickCode: fmt.Sprint(d["pc"]),
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

// syncAssetFile 下载媒体附属文件（图片/字幕/nfo）为本地真实文件，保持目录结构
// Emby/Jellyfin 无法通过 .strm 读取海报和字幕，附属文件必须实体存在；已存在则跳过
func syncAssetFile(ops *pan115Ops, f remoteFile, localRoot string) (string, error) {
	dir := filepath.Join(localRoot, filepath.FromSlash(f.Path))
	dst := filepath.Join(dir, f.Name)
	if _, err := os.Stat(dst); err == nil {
		return "skip", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if f.PickCode == "" || f.PickCode == "<nil>" {
		return "", fmt.Errorf("缺少 pickcode")
	}
	dlURL, err := ops.downloadURL(f.PickCode)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodGet, dlURL, nil)
	if err != nil {
		return "", err
	}
	// 115 下载直链与请求时的 UA 绑定，必须用统一 UA 拉取
	req.Header.Set("User-Agent", ua115Unified())
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, werr := io.Copy(out, resp.Body)
	cerr := out.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("写入本地失败")
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return "download", nil
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
	form := url.Values{
		"aid":  {"1"},
		"pid":  {parentCid},
		"cid":  {parentCid},
		"n":    {folderName},
	}
	body, err := httpPostForm115("https://webapi.115.com/files/add", form, cookie, 15*time.Second)
	if err != nil {
		return "", err
	}
	var result struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			Cid  string `json:"cid"`
			FID  string `json:"fid"`
		} `json:"data"`
		ErrNo  int    `json:"errNo"`
		ErrMsg string `json:"errMsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析创建目录响应失败")
	}
	if !result.State {
		// 目录可能已存在
		if result.ErrMsg != "" {
			return "", fmt.Errorf("创建目录失败: %s", result.ErrMsg)
		}
		return "", fmt.Errorf("创建目录失败: %s", result.Error)
	}
	if result.Data.Cid != "" {
		return result.Data.Cid, nil
	}
	return result.Data.FID, nil
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
	form := url.Values{
		"pid": {targetCid},
	}
	for _, fid := range fileIds {
		form.Add("fid", fid)
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
	evFolderRename = "folder_rename"    // 20 目录改名
	evDelete      = "delete_file"       // 22 删除文件/目录
	evCopy        = "copy_file"         // 23 复制文件
	evRename      = "file_rename"       // 24 文件改名
)

// lifeEvent 一条生活事件
type lifeEvent struct {
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
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析生活事件失败")
	}
	events := make([]lifeEvent, 0, len(result.Data))
	for _, d := range result.Data {
		ev := lifeEvent{
			Type:     normalizeEventType(fmt.Sprint(d["type"])),
			FileID:   firstStr(d, "file_id", "fid", "id"),
			FileName: firstStr(d, "file_name", "n", "name"),
			Cid:      firstStr(d, "cid", "pid", "parent_id"),
			Time:     firstStr(d, "time", "update_time", "create_time"),
		}
		if s, ok := d["file_size"].(float64); ok {
			ev.Size = int64(s)
		}
		events = append(events, ev)
	}
	return events, nil
}

// RunIncrementalSync 增量同步：拉取生活事件，处理媒体库文件变化
// POST /sync/incremental  body: {"cid":"...","local_path":"...","video_ext":["mp4","mkv"],"limit":1000}
func (h *Handler) RunIncrementalSync(c *gin.Context) {
	var req struct {
		Cid       string   `json:"cid"`
		LocalPath string   `json:"local_path"`
		VideoExt  []string `json:"video_ext"`
		Limit     int      `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 1000
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 拉取生活事件（全部类型）
	events, err := fetch115LifeEvents(cookie, req.Limit, 0, "")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "拉取生活事件失败: " + err.Error()})
		return
	}

	// 过滤出与媒体文件相关的操作
	exts := buildExtSet(req.VideoExt)
	relevant := make([]lifeEvent, 0)
	for _, ev := range events {
		switch ev.Type {
		case evUpload, evReceive, evCopy, evMove, evDelete, evRename:
			// 只看视频后缀文件；删除/移动事件可能无后缀信息，也纳入
			ext := strings.ToLower(path.Ext(ev.FileName))
			if ext != "" && len(exts) > 0 && !exts[ext] {
				continue
			}
			relevant = append(relevant, ev)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "生活事件拉取成功",
		"total":    len(events),
		"relevant": len(relevant),
		"events":   relevant,
	})
}

// ==================== 全量同步 ====================

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

	// 视频 → 生成 STRM
	strmCreated := 0
	for _, f := range videos {
		if err := writeStrm(req.LocalPath, domain, format, keepExt, skipExist, f); err != nil {
			continue
		}
		strmCreated++
	}

	// 附属文件 → 下载实体文件（已存在跳过）
	downloaded, skipped, failed := 0, 0, 0
	for i, f := range assets {
		if i%20 == 0 {
			log.Printf("[115同步] 附属文件进度: %d/%d", i, len(assets))
		}
		st, err := syncAssetFile(ops, f, req.LocalPath)
		switch {
		case err != nil:
			failed++
			log.Printf("[115同步] 附属文件失败: %s/%s: %v", f.Path, f.Name, err)
		case st == "skip":
			skipped++
		default:
			downloaded++
		}
	}

	totalNew := strmCreated + downloaded
	if totalNew > 0 {
		h.notifyEmbyRefresh(req.LocalPath)
	}
	log.Printf("[115同步] 全量同步完成: 视频 %d（生成 STRM %d），附属文件 %d（下载 %d，跳过 %d，失败 %d）",
		len(videos), strmCreated, len(assets), downloaded, skipped, failed)

	c.JSON(http.StatusOK, gin.H{
		"message": "全量同步完成",
		"total":   len(videos),
		"created": strmCreated,
		"assets_total":      len(assets),
		"assets_downloaded": downloaded,
		"assets_skipped":    skipped,
		"assets_failed":     failed,
	})
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

// RunOrganizePipeline 整理→同步闭环
// POST /organize/pipeline  body: {"sync_after": true}
// 1. 加载整理配置
// 2. 整理引擎：TMDB识别→去重→重命名→分类
// 3. 对已存在文件夹执行全量同步生成 STRM
func (h *Handler) RunOrganizePipeline(c *gin.Context) {
	var req struct {
		SyncAfter bool `json:"sync_after"`
	}
	c.ShouldBindJSON(&req)

	result := gin.H{"steps": []gin.H{}}
	steps := []gin.H{}

	// Step 1: 加载整理配置
	orgCfg, err := loadOrgConfig()
	if err != nil {
		steps = append(steps, gin.H{"step": "整理", "status": "跳过", "message": err.Error()})
		result["steps"] = steps
		result["message"] = err.Error()
		c.JSON(http.StatusOK, result)
		return
	}

	// Step 2: 构造统一操作通道（OpenAPI 优先，Cookie 回退）
	ops, err := h.newPan115Ops()
	if err != nil {
		steps = append(steps, gin.H{"step": "整理", "status": "失败", "message": err.Error()})
		result["steps"] = steps
		result["message"] = err.Error()
		c.JSON(http.StatusOK, result)
		return
	}

	// Step 3: 运行整理引擎
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
	result["organize_results"] = orgResults
	result["organize_total"] = totalFiles
	result["organize_success"] = successCount

	// Step 4: 对已存在文件夹执行全量同步生成 STRM
	if req.SyncAfter {
		// 读取全量同步配置
		var syncSetting model.Setting
		h.DB.Where("key = ?", "full").First(&syncSetting)
		var syncCfg struct {
			LocalPath string   `json:"local_path"`
			VideoExt  []string `json:"video_ext"`
		}
		if syncSetting.Value != "" {
			json.Unmarshal([]byte(syncSetting.Value), &syncCfg)
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
		var files []remoteFile
		if err := walk115Dir(ops, orgCfg.Library, "", &files, nil, filter); err != nil {
			steps = append(steps, gin.H{"step": "STRM 同步", "status": "失败", "message": "遍历目录失败: " + err.Error()})
			result["steps"] = steps
			result["message"] = "整理完成，但同步失败"
			c.JSON(http.StatusOK, result)
			return
		}

		count := 0
		for _, f := range files {
			if err := writeStrm(syncCfg.LocalPath, domain, format, keepExt, skipExist, f); err != nil {
				continue
			}
			count++
		}
		steps = append(steps, gin.H{"step": "STRM 同步", "status": "完成", "message": fmt.Sprintf("共 %d 个视频，生成 %d 个 STRM", len(files), count)})
		result["strm_total"] = len(files)
		result["strm_created"] = count

		// 通知 Emby 刷新
		if count > 0 {
			h.notifyEmbyRefresh(syncCfg.LocalPath)
		}
	}

	result["steps"] = steps
	result["message"] = "整理→同步闭环完成"

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

	c.JSON(http.StatusOK, result)
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

	// 调用 Emby API 刷新媒体库（不带 API Key，依赖 Emby 的局域网开放访问）
	// POST /Library/Media/Updated { "Updates": [{"Path":"...","UpdateType":"Created"}] }
	body, _ := json.Marshal(map[string]interface{}{
		"Updates": []map[string]string{
			{"Path": embyPath, "UpdateType": "Created"},
		},
	})
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", embyServer+"/Library/Media/Updated", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Emby 刷新通知失败: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("Emby 刷新通知已发送: %s", embyPath)
}
