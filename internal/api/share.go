package api

// ==================== 115 分享链接转存 ====================
//
// 流程（web 通道经典四步）：
//  1. 解析分享链接取 share_code
//  2. POST webapi.115.com/share/info 拿分享信息
//  3. POST webapi.115.com/share/snap 拿文件列表
//  4. POST webapi.115.com/share/sharepost + files/receive 转存到目标目录
//
// 转存后由「自动整理 → 增量同步」闭环接管（转存产生 receive_files 事件）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ShareReceive 转存分享链接到接收文件夹
// POST /share/receive  body: {"url":"https://115.com/s/xxx", "code":"提取码", "target_cid":"可选，默认接收文件夹"}

// ==================== 分享接口镜像轮换 ====================
//
// 分享三接口（info/snap/sharepost）此前直连 webapi.115.com，被风控
// （"服务器开小差了"）时整个分享转存失败。与文件列表接口同款思路：
// 主域名被拒时轮换镜像域名重试（web.api / 115cdn / 115vod）。

var shareAPIOrigins = []string{
	"https://webapi.115.com",
	"http://web.api.115.com",
	"https://115cdn.com/webapi",
	"https://115vod.com/webapi",
}

// postShareAPI 分享接口 POST：请求失败或命中 115 风控响应（开小差/频繁）
// 时切换下一镜像，全部镜像用尽后返回最后一次结果
func postShareAPI(path string, form url.Values, cookie string, timeout time.Duration) ([]byte, error) {
	var lastBody []byte
	var lastErr error
	for _, origin := range shareAPIOrigins {
		body, err := httpPostForm115(origin+path, form, cookie, timeout)
		if err != nil {
			lastErr = err
			continue
		}
		if is115BusyResp(body) {
			lastBody = body
			log.Printf("[上传] ○ %s 命中风控（开小差），切换镜像重试", path)
			continue
		}
		return body, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return lastBody, nil
}

// is115BusyResp 识别 115 风控响应（state=false 且带"开小差/稍后再试/频繁"文案）
func is115BusyResp(body []byte) bool {
	if !strings.Contains(string(body), "\"state\":false") {
		return false
	}
	return strings.Contains(string(body), "开小差") ||
		strings.Contains(string(body), "稍后再试") ||
		strings.Contains(string(body), "频繁")
}

func (h *Handler) ShareReceive(c *gin.Context) {
	var req struct {
		URL      string `json:"url"`
		Code     string `json:"code"`
		Target   string `json:"target_cid"`
		Organize bool   `json:"organize"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写分享链接"})
		return
	}
	if req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写提取码"})
		return
	}
	msg, success, fail, err := h.shareReceiveCore(req.URL, req.Code, req.Target, req.Organize)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if success == 0 && fail == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "分享为空", "count": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "count": success, "failed": fail,
		"note": "转存成功，增量同步已自动触发（约 30 秒后完成 STRM 生成）"})
}

// shareReceiveCore 转存核心（HTTP 接口与企微机器人共用）：
// 解析分享码 → info → snap（翻页收全）→ sharepost → 逐项 receive；organize=true 时转存后触发整理+增量
func (h *Handler) shareReceiveCore(shareURL, code, target string, organize bool) (msg string, success, fail int, err error) {
	shareCode := extractShareCode(shareURL)
	if shareCode == "" {
		return "", 0, 0, fmt.Errorf("无法从链接解析分享码")
	}
	// 提取码允许为空：无提取码分享直接转存（影巢解锁的资源可能不带访问码）
	// 目标目录：参数优先，否则取分享同步配置的接收文件夹
	if target == "" {
		var cfg struct {
			Folder string `json:"folder"`
		}
		if err := json.Unmarshal([]byte(h.getSettingValue("share")), &cfg); err != nil {
			log.Printf("[上传] ○ 分享配置解析失败: %v", err)
		}
		target = cfg.Folder
	}
	if target == "" {
		return "", 0, 0, fmt.Errorf("未配置接收文件夹（分享同步卡）")
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		return "", 0, 0, err
	}
	req := struct {
		Organize bool
	}{organize}

	log.Printf("[上传] ▶ 分享转存开始: %s（提取码 %q）", truncateStr(shareURL, 70), code)

	// 1. 分享信息（校验链接与提取码）
	infoBody, err := postShareAPI("/share/info",
		url.Values{"share_code": {shareCode}}, cookie, 15*time.Second)
	if err != nil {
		log.Printf("[上传] ✗ 获取分享信息失败: %v", err)
		return "", 0, 0, fmt.Errorf("获取分享信息失败: %s", err.Error())
	}
	var info struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			ShareTitle string `json:"share_title"`
		} `json:"data"`
	}
	if json.Unmarshal(infoBody, &info) != nil || !info.State {
		log.Printf("[上传] ✗ 分享信息校验失败: %s", truncateStr(string(infoBody), 120))
		return "", 0, 0, fmt.Errorf("分享信息校验失败: %s", truncateStr(string(infoBody), 120))
	}

	// 2. 文件列表（顶层收全部；1150/页翻页收取——此前只取第一页，
	// 超过 1150 项的分享被静默截断收不全）
	type snapItem struct {
		Fid string `json:"fid"`
		Fn  string `json:"fn"`
		Fc  int    `json:"fc"`
		Cid string `json:"cid"`
	}
	var allItems []snapItem
	for offset := 0; ; offset += 1150 {
		snapBody, err := postShareAPI("/share/snap",
			url.Values{
				"share_code":   {shareCode},
				"receive_code": {code},
				"cid":          {"0"},
				"offset":       {fmt.Sprint(offset)},
				"limit":        {"1150"},
				"asc":          {"1"},
				"fc_mix":       {"0"},
			}, cookie, 15*time.Second)
		if err != nil {
			return "", 0, 0, fmt.Errorf("获取分享文件列表失败: %s", err.Error())
		}
		var snap struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Data  struct {
				List []snapItem `json:"list"`
			} `json:"data"`
		}
		if json.Unmarshal(snapBody, &snap) != nil || !snap.State {
			log.Printf("[上传] ✗ 文件列表获取失败（提取码错误？）: %s", truncateStr(string(snapBody), 120))
			return "", 0, 0, fmt.Errorf("文件列表获取失败（提取码错误？）: %s", truncateStr(string(snapBody), 120))
		}
		allItems = append(allItems, snap.Data.List...)
		if len(snap.Data.List) < 1150 {
			break // 最后一页
		}
	}
	if len(allItems) == 0 {
		return "分享为空", 0, 0, nil
	}
	log.Printf("[上传] ▣ 分享「%s」共 %d 项，开始转存...", info.Data.ShareTitle, len(allItems))

	// 3. sharepost 拿 pick_code
	form := url.Values{
		"share_code":   {shareCode},
		"receive_code": {code},
	}
	for i, f := range allItems {
		form.Set(fmt.Sprintf("file_id[%d]", i), f.Fid)
	}
	postBody, err := postShareAPI("/share/sharepost", form, cookie, 20*time.Second)
	if err != nil {
		return "", 0, 0, fmt.Errorf("sharepost 失败: %s", err.Error())
	}
	var post struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			List []struct {
				Fid      string `json:"fid"`
				PickCode string `json:"pick_code"`
				FileName string `json:"file_name"`
			} `json:"list"`
		} `json:"data"`
	}
	if json.Unmarshal(postBody, &post) != nil || !post.State {
		log.Printf("[上传] ✗ sharepost 被拒: %s", truncateStr(string(postBody), 120))
		return "", 0, 0, fmt.Errorf("sharepost 被拒: %s", truncateStr(string(postBody), 120))
	}

	// 4. 逐个转存到目标目录
	userid := cookieUserID(cookie)
	success, fail = 0, 0
	for _, item := range post.Data.List {
		rForm := url.Values{
			"user_id":   {fmt.Sprint(userid)},
			"file_id":   {item.Fid},
			"pick_code": {item.PickCode},
			"cid":       {target},
		}
		rBody, err := httpPostForm115("https://webapi.115.com/files/receive", rForm, cookie, 20*time.Second)
		if err != nil {
			fail++
			log.Printf("[上传] ✗ 转存失败: %s: %v", item.FileName, err)
			continue
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rBody, &r)
		if r.State {
			success++
			log.Printf("[上传] ✓ 转存: %s", item.FileName)
		} else {
			fail++
			log.Printf("[上传] ✗ 转存被拒: %s: %s", item.FileName, r.Error)
		}
		time.Sleep(500 * time.Millisecond)
	}

	msg = fmt.Sprintf("「%s」转存完成: 成功 %d，失败 %d（共 %d 项）", info.Data.ShareTitle, success, fail, len(post.Data.List))
	log.Printf("[上传] %s", msg)

	// 转存成功且开启自动整理 → 触发「整理+增量」
	if success > 0 && req.Organize {
		go h.triggerOrganizeAndSync()
	}
	return msg, success, fail, nil
}

// re115Share 115 分享链接（含 115cdn / anxia 新域名）；分享码可能带 - _
var re115Share = regexp.MustCompile(`(?:115\.com|115cdn\.com|anxia\.com)/s/([a-zA-Z0-9_-]+)`)

// reSharePass 分享链接内嵌提取码：?password=xxxx 或 #xxxx
var reSharePass = regexp.MustCompile(`(?:[?&]password=|#)([A-Za-z0-9]+)`)

// extractShareCode 从分享链接提取 share_code
func extractShareCode(raw string) string {
	if m := re115Share.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return strings.TrimSpace(raw)
}

// is115ShareLink 判断链接是否为 115 分享（可自动转存的域）
func is115ShareLink(raw string) bool {
	return re115Share.MatchString(raw)
}
