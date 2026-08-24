package api

// ==================== 115 文件上传 + 监控上传引擎 ====================
//
// 上传流程（p115client upload_init + p115oss OSS 直传）：
//  1. POST proapi.115.com/open/upload/init（fileid=sha1, filename, filesize,
//     target=U_1_{pid}, userid）→ 秒传直接完成，否则返回 OSS 信息
//  2. GET uplb.115.com/3.0/gettoken.php → OSS 临时凭证
//  3. PUT https://{bucket}.oss-cn-shenzhen.aliyuncs.com/{object}，
//     带 OSS 签名 + x-oss-callback（服务端回调确认入库）
//
// 监控上传（CMS media_moni 同款）：定期扫描本地媒体目录中 Emby 新生成的
// 图片，按目录结构对应上传到 115 剧集目录（本地 strm 结构与网盘一致）。

import (
	"crypto/hmac"
	"crypto/sha1"
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
	"sort"
	"strings"
	"time"
)

// uploadInitResp /open/upload/init 响应
type uploadInitResp struct {
	State  bool   `json:"state"`
	Error  string `json:"error"`
	ErrNo  int    `json:"errNo"`
	Status int    `json:"status"`
	Data   struct {
		Bucket   string `json:"bucket"`
		Object   string `json:"object"`
		FileID   string `json:"file_id"`
		Callback struct {
			URL  string `json:"url"`
			Body string `json:"body"`
			Type string `json:"callback_body_type"`
		} `json:"callback"`
	} `json:"data"`
}

// ossTokenResp gettoken.php 响应
type ossTokenResp struct {
	State bool `json:"state"`
	Token struct {
		AccessKeyID     string `json:"AccessKeyId"`
		AccessKeySecret string `json:"AccessKeySecret"`
		SecurityToken   string `json:"SecurityToken"`
		Expiration      string `json:"Expiration"`
	} `json:"token"`
}

// upload115File 上传文件内容到 115 指定目录（秒传或 OSS 直传）
func upload115File(cookie string, pid int64, userid int64, filename string, data []byte) error {
	sha := fmt.Sprintf("%x", sha1.Sum(data))

	// 1. init（秒传或获取 OSS 参数）
	form := url.Values{
		"fileid":   {strings.ToUpper(sha)},
		"filename": {filename},
		"filesize": {fmt.Sprint(len(data))},
		"target":   {fmt.Sprintf("U_1_%d", pid)},
		"userid":   {fmt.Sprint(userid)},
	}
	body, err := post115Form("https://proapi.115.com/open/upload/init", form, cookie, ua115Download, 20*time.Second)
	if err != nil {
		return fmt.Errorf("upload init 失败: %w", err)
	}
	var init uploadInitResp
	if err := json.Unmarshal(body, &init); err != nil || !init.State {
		return fmt.Errorf("upload init 被拒: %s", truncateStr(string(body), 150))
	}
	if init.Data.Bucket == "" || init.Data.Object == "" {
		return nil // 秒传命中（115 已有相同文件）
	}

	// 2. OSS 临时凭证
	tkBody, err := httpGet115UA("https://uplb.115.com/3.0/gettoken.php", nil, cookie, ua115Download, 15*time.Second)
	if err != nil {
		return fmt.Errorf("gettoken 失败: %w", err)
	}
	var tk ossTokenResp
	if json.Unmarshal(tkBody, &tk) != nil || !tk.State || tk.Token.AccessKeyID == "" {
		return fmt.Errorf("gettoken 响应异常: %s", truncateStr(string(tkBody), 120))
	}

	// 3. OSS PUT（带回调，由 115 服务端确认入库）
	return ossPut(init, tk.Token, data)
}

// ossPut 阿里云 OSS PUT 上传（Aliyun OSS 签名规范）
func ossPut(init uploadInitResp, tk struct {
	AccessKeyID     string `json:"AccessKeyId"`
	AccessKeySecret string `json:"AccessKeySecret"`
	SecurityToken   string `json:"SecurityToken"`
	Expiration      string `json:"Expiration"`
}, data []byte) error {
	host := init.Data.Bucket + ".oss-cn-shenzhen.aliyuncs.com"
	resource := "/" + init.Data.Bucket + "/" + init.Data.Object
	date := time.Now().UTC().Format(http.TimeFormat)
	contentType := "application/octet-stream"

	// 回调头（base64 的回调配置）
	cb := map[string]string{
		"callbackUrl":         init.Data.Callback.URL,
		"callbackBody":        init.Data.Callback.Body,
		"callbackBodyType":    "application/x-www-form-urlencoded",
	}
	cbJSON, _ := json.Marshal(cb)
	cbHeader := base64.StdEncoding.EncodeToString(cbJSON)

	// OSS 签名：VERB\nMD5\nContentType\nDate\nCanonicalizedOSSHeaders+Resource
	stringToSign := "PUT\n\n" + contentType + "\n" + date + "\n" +
		"x-oss-callback:" + cbHeader + "\nx-oss-security-token:" + tk.SecurityToken + "\n" + resource
	mac := hmac.New(sha1.New, []byte(tk.AccessKeySecret))
	mac.Write([]byte(stringToSign))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPut, "https://"+host+"/"+init.Data.Object, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-oss-security-token", tk.SecurityToken)
	req.Header.Set("x-oss-callback", cbHeader)
	req.Header.Set("Authorization", "OSS "+tk.AccessKeyID+":"+sign)

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("OSS PUT 失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 203 {
		return fmt.Errorf("OSS PUT HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 150))
	}
	return nil
}

// ==================== 监控上传引擎 ====================

var uploadedMark = map[string]bool{} // 已上传文件路径（进程内标记，避免重复上传）

// StartMonitorUploader 启动监控上传：定期扫描监控目录中新生成的图片，
// 按本地目录结构对应上传到 115 媒体库（本地 strm 树与网盘一致）
func StartMonitorUploader(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				monitorOnce(h)
			case <-stopCh:
				return
			}
		}
	}()
	log.Println("[监控上传] 引擎已启动（每分钟扫描一次监控目录）")
}

// monitorOnce 单轮扫描上传
func monitorOnce(h *Handler) {
	// 配置：仅需监控目录；上传目标固定为全量同步的媒体库（旧配置里的
	// target 字段已废弃忽略——监控的是本地媒体树，目标自然是云端媒体库）
	var cfg struct {
		Dir    string `json:"dir"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(h.getSettingValue("monitor")), &cfg); err != nil {
		return // 配置解析失败视为未启用
	}
	if cfg.Dir == "" {
		return // 未启用
	}
	_ = cfg.Target

	cookie, err := h.get115Cookie()
	if err != nil {
		return
	}
	userid := cookieUserID(cookie)

	// 目标库根 cid 与绝对路径
	rootCid := cfg.Target
	var fullCfg struct {
		Cid        string `json:"cid"`
		LocalPath  string `json:"local_path"`
	}
	if err := json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg); err != nil {
		return
	}
	if rootCid == "" {
		rootCid = fullCfg.Cid
	}
	if rootCid == "" {
		return
	}
	libAbs := absPathOf(cookie, rootCid, map[string]dirInfo{})
	if libAbs == "" {
		return
	}

	// 扫描监控目录：Emby 刮削产物（标准图片命名 + NFO），只处理最近 24h 内的新文件
	var imgs []string
	cutoff := time.Now().Add(-24 * time.Hour)
	filepath.WalkDir(cfg.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		ext := strings.ToLower(filepath.Ext(p))
		isImg := (ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp") &&
			isStandardMediaImageName(name)
		isNfo := strings.HasSuffix(name, ".nfo")
		if !isImg && !isNfo {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) && !uploadedMark[p] {
			imgs = append(imgs, p)
		}
		return nil
	})
	if len(imgs) == 0 {
		return
	}
	sort.Strings(imgs)
	log.Printf("[上传] 发现 %d 个新刮削文件（图片+NFO）", len(imgs))

	for _, img := range imgs {
		rel, err := filepath.Rel(cfg.Dir, img)
		if err != nil {
			continue
		}
		relDir := filepath.ToSlash(filepath.Dir(rel))
		// 定位 115 目标目录：媒体库绝对路径 + 相对目录（files/getid 查询）
		targetAbs := strings.TrimSuffix(libAbs, "/") + "/" + strings.TrimPrefix(relDir, "./")
		cid, ok := cloudPathCid(cookie, targetAbs)
		if !ok {
			log.Printf("[上传] 未找到对应 115 目录，跳过 %s（%s）", rel, targetAbs)
			continue
		}
		data, err := os.ReadFile(img)
		if err != nil {
			continue
		}
		if err := upload115File(cookie, parseI64(cid), userid, filepath.Base(img), data); err != nil {
			log.Printf("[上传] ✗ 上传失败 %s: %v", rel, err)
			continue
		}
		uploadedMark[img] = true
		log.Printf("[上传] ✓ 上传成功: %s → %s", rel, targetAbs)
	}
}

// metadataUploadNames 元数据回传监听的文件名（Emby 写入媒体目录的标准名）
var metadataUploadNames = map[string]bool{
	"poster.jpg": true, "poster.jpeg": true, "poster.png": true,
	"fanart.jpg": true, "fanart.jpeg": true, "banner.jpg": true,
	"tvshow.nfo": true, "movie.nfo": true, "season.nfo": true,
}

// StartMetadataUploader 元数据回传引擎：每 5 分钟扫描本地媒体树，
// 把 Emby 写入的 poster/nfo/fanart 上传到 115 对应目录（按相对路径
// 用 files/getid 定位）。与「监控上传」（Emby 图片目录→115）互补：
// 这里针对的是媒体卷内、随剧集目录存放的元数据文件
func StartMetadataUploader(h *Handler) {
	// 监控上传（monitor 配置）已覆盖同一职责且更可配（目录自选）；
	// 本引擎仅在监控目录未配置时作为兜底启用，避免双路重复上传
	var mc struct {
		Dir string `json:"dir"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("monitor")), &mc)
	if mc.Dir != "" {
		return // 监控上传已启用，兜底引擎休眠
	}
	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(5 * time.Minute):
			}
			h.uploadMetadataOnce()
		}
	}()
	log.Println("[元数据回传] 兜底引擎已启动（未配置监控目录；每 5 分钟扫描媒体目录）")
}

// uploadMetadataOnce 单轮回传
func (h *Handler) uploadMetadataOnce() {
	local := defaultLocalPath
	var fullCfg struct {
		LocalPath string `json:"local_path"`
		Cid       string `json:"cid"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg) == nil && fullCfg.LocalPath != "" {
		local = fullCfg.LocalPath
	}
	if fullCfg.Cid == "" {
		return // 未配置媒体库，无法定位云端目录
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return
	}
	userid := cookieUserID(cookie)

	// 库根绝对路径与库名（云端目录定位用）。
	// 本地路径第一层是库名（STRM 结构特性），拼接云端绝对路径前要剥掉，
	// 否则出现 /俱乐部/俱乐部/... 查不到目录（全部跳过）
	libAbs := absPathOf(cookie, fullCfg.Cid, map[string]dirInfo{})
	if libAbs == "" {
		return
	}
	libName := ""
	if info, err := get115DirInfo(cookie, fullCfg.Cid); err == nil {
		libName = info.n
	}

	// 扫描媒体树里的元数据文件（跳过已上传标记；用台账判定是否已在云端）
	var files []string
	filepath.WalkDir(local, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !metadataUploadNames[strings.ToLower(d.Name())] {
			// 每集同名 nfo（xxx.mkv.nfo）也要回传
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv.nfo") &&
				!strings.HasSuffix(strings.ToLower(d.Name()), ".mp4.nfo") {
				return nil
			}
		}
		if metaUploadedMark[p] {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if len(files) == 0 {
		return
	}
	log.Printf("[元数据回传] 发现 %d 个待上传文件", len(files))

	ok, fail := 0, 0
	for _, f := range files {
		rel, err := filepath.Rel(local, f)
		if err != nil {
			continue
		}
		relDir := filepath.ToSlash(filepath.Dir(rel))
		// 剥掉本地路径的库名第一层，与云端库根对齐
		if libName != "" {
			relDir = strings.TrimPrefix(strings.TrimPrefix(relDir, libName), "/")
		}
		targetAbs := strings.TrimSuffix(libAbs, "/")
		if relDir != "" && relDir != "." {
			targetAbs += "/" + relDir
		}
		cid, found := cloudPathCid(cookie, targetAbs)
		if !found {
			log.Printf("[元数据回传] ○ 云端目录不存在，跳过 %s（%s）", rel, targetAbs)
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if err := upload115File(cookie, parseI64(cid), userid, filepath.Base(f), data); err != nil {
			fail++
			log.Printf("[元数据回传] ✗ 失败 %s: %v", rel, err)
			continue
		}
		metaUploadedMark[f] = true
		ok++
		log.Printf("[元数据回传] ✓ %s → %s", rel, targetAbs)
	}
	if ok+fail > 0 {
		log.Printf("[元数据回传] 本轮完成: 成功 %d，失败 %d", ok, fail)
	}
}

// metaUploadedMark 已回传路径标记（会话级）
var metaUploadedMark = map[string]bool{}

// isStandardMediaImageName Emby/Jellyfin 标准媒体图片命名
//（poster/fanart/banner/logo/clearart/disc…及 seasonXX-poster 等变体）
func isStandardMediaImageName(lowerName string) bool {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(
		strings.TrimSuffix(strings.TrimSuffix(lowerName, ".jpg"), ".jpeg"),
		".png"), ".webp"), "")
	for _, std := range []string{"poster", "fanart", "backdrop", "banner", "thumb",
		"landscape", "logo", "clearlogo", "clearart", "disc", "discart", "folder"} {
		if base == std || strings.HasPrefix(base, std+"-") || strings.HasSuffix(base, "-"+std) {
			return true
		}
	}
	// seasonXX-poster / season-specials-poster 变体
	if regexp.MustCompile(`^season(\d{1,2}|specials)-.+`).MatchString(base) {
		return true
	}
	// 剧集名-poster 等含分隔符的形态已由前后缀匹配覆盖
	return false
}

// cloudPathCid 按绝对路径查询 115 目录 cid（files/getid）
func cloudPathCid(cookie, absPath string) (string, bool) {
	body, err := httpGet115UA("https://webapi.115.com/files/getid",
		url.Values{"path": {absPath}}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return "", false
	}
	var r struct {
		State bool `json:"state"`
		Data  []struct {
			Cid string `json:"cid"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State || len(r.Data) == 0 {
		return "", false
	}
	return r.Data[0].Cid, true
}

// cookieUserID 从 Cookie 的 UID 字段提取用户 id（UID=xxx_格式）
func cookieUserID(cookie string) int64 {
	for _, part := range strings.Split(cookie, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == "UID" {
			idStr := strings.SplitN(kv[1], "_", 2)[0]
			var id int64
			fmt.Sscanf(idStr, "%d", &id)
			return id
		}
	}
	return 0
}

func parseI64(s string) int64 {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}
