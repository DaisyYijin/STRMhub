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
	"log"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ShareReceive 转存分享链接到接收文件夹
// POST /share/receive  body: {"url":"https://115.com/s/xxx", "code":"提取码", "target_cid":"可选，默认接收文件夹"}
func (h *Handler) ShareReceive(c *gin.Context) {
	var req struct {
		URL    string `json:"url"`
		Code   string `json:"code"`
		Target string `json:"target_cid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写分享链接"})
		return
	}
	shareCode := extractShareCode(req.URL)
	if shareCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法从链接解析分享码"})
		return
	}
	if req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写提取码"})
		return
	}
	// 目标目录：参数优先，否则取分享同步配置的接收文件夹
	if req.Target == "" {
		var cfg struct {
			Folder string `json:"folder"`
		}
		_ = json.Unmarshal([]byte(h.getSettingValue("share")), &cfg)
		req.Target = cfg.Folder
	}
	if req.Target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置接收文件夹（分享同步卡）"})
		return
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 1. 分享信息（校验链接与提取码）
	infoBody, err := httpPostForm115("https://webapi.115.com/share/info",
		url.Values{"share_code": {shareCode}}, cookie, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取分享信息失败: " + err.Error()})
		return
	}
	var info struct {
		State bool `json:"state"`
		Error string `json:"error"`
		Data  struct {
			ShareTitle string `json:"share_title"`
		} `json:"data"`
	}
	if json.Unmarshal(infoBody, &info) != nil || !info.State {
		c.JSON(http.StatusBadGateway, gin.H{"error": "分享信息校验失败: " + truncateStr(string(infoBody), 120)})
		return
	}

	// 2. 文件列表（顶层，收全部）
	snapBody, err := httpPostForm115("https://webapi.115.com/share/snap",
		url.Values{
			"share_code":  {shareCode},
			"receive_code": {req.Code},
			"cid":         {"0"},
			"offset":      {"0"},
			"limit":       {"1150"},
			"asc":         {"1"},
			"fc_mix":      {"0"},
		}, cookie, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取分享文件列表失败: " + err.Error()})
		return
	}
	var snap struct {
		State bool `json:"state"`
		Error string `json:"error"`
		Data  struct {
			List []struct {
				Fid  string `json:"fid"`
				Fn  string `json:"fn"`
				Fc  int    `json:"fc"`
				Cid string `json:"cid"`
			} `json:"list"`
		} `json:"data"`
	}
	if json.Unmarshal(snapBody, &snap) != nil || !snap.State {
		c.JSON(http.StatusBadGateway, gin.H{"error": "文件列表获取失败（提取码错误？）: " + truncateStr(string(snapBody), 120)})
		return
	}
	if len(snap.Data.List) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "分享为空", "count": 0})
		return
	}

	// 3. sharepost 拿 pick_code
	form := url.Values{
		"share_code":  {shareCode},
		"receive_code": {req.Code},
	}
	for i, f := range snap.Data.List {
		form.Set(fmt.Sprintf("file_id[%d]", i), f.Fid)
	}
	postBody, err := httpPostForm115("https://webapi.115.com/share/sharepost", form, cookie, 20*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "sharepost 失败: " + err.Error()})
		return
	}
	var post struct {
		State bool `json:"state"`
		Error string `json:"error"`
		Data  struct {
			List []struct {
				Fid       string `json:"fid"`
				PickCode  string `json:"pick_code"`
				FileName  string `json:"file_name"`
			} `json:"list"`
		} `json:"data"`
	}
	if json.Unmarshal(postBody, &post) != nil || !post.State {
		c.JSON(http.StatusBadGateway, gin.H{"error": "sharepost 被拒: " + truncateStr(string(postBody), 120)})
		return
	}

	// 4. 逐个转存到目标目录
	userid := cookieUserID(cookie)
	success, fail := 0, 0
	for _, item := range post.Data.List {
		rForm := url.Values{
			"user_id":   {fmt.Sprint(userid)},
			"file_id":   {item.Fid},
			"pick_code": {item.PickCode},
			"cid":       {req.Target},
		}
		rBody, err := httpPostForm115("https://webapi.115.com/files/receive", rForm, cookie, 20*time.Second)
		if err != nil {
			fail++
			continue
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rBody, &r)
		if r.State {
			success++
		} else {
			fail++
		}
		time.Sleep(500 * time.Millisecond)
	}

	msg := fmt.Sprintf("「%s」转存完成: 成功 %d，失败 %d（共 %d 项）", info.Data.ShareTitle, success, fail, len(post.Data.List))
	log.Printf("[上传] %s", msg)
	c.JSON(http.StatusOK, gin.H{"message": msg, "count": success, "failed": fail,
		"note": "转存内容若在待整理目录，将由下一轮自动整理+增量同步接管"})
}

// extractShareCode 从分享链接提取 share_code
func extractShareCode(raw string) string {
	m := regexp.MustCompile(`115\.com/s/([a-zA-Z0-9]+)`).FindStringSubmatch(raw)
	if m != nil {
		return m[1]
	}
	return strings.TrimSpace(raw)
}
