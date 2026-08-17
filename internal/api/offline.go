package api

// ==================== 115 离线下载（磁力/ed2k/HTTP）====================
//
// POST https://clouddownload.115.com/lixianssp/?ac=add_task_url
// 认证：Cookie + data=RSA加密载荷（复用 115crypto 的 encrypt115）
// UA：  Mozilla/5.0 115disk/{ver} 115Browser/{ver} 115wangpan_android/{ver}

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// offlineAddTask 添加离线下载任务（磁力/ed2k/HTTP/FTP）
// POST /offline/add  body: {"url":"magnet:?xt=...", "target_cid":"可选"}
func (h *Handler) offlineAddTask(c *gin.Context) {
	var req struct {
		URL    string `json:"url"`
		Target string `json:"target_cid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写链接"})
		return
	}

	// 验证链接类型
	linkType := classifyLink(req.URL)
	if linkType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的链接类型（仅支持磁力/ed2k/HTTP/FTP）"})
		return
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 目标目录：参数优先，否则用分享同步配置的接收文件夹
	if req.Target == "" {
		var cfg struct {
			Folder string `json:"folder"`
		}
		_ = json.Unmarshal([]byte(h.getSettingValue("share")), &cfg)
		req.Target = cfg.Folder
	}

	// 构造载荷
	payload := map[string]string{
		"url": req.URL,
	}
	if req.Target != "" {
		payload["wp_path_id"] = req.Target
	}

	// 加密并发送
	ver := getAppVerCached()
	ua := fmt.Sprintf("Mozilla/5.0 115disk/%s 115Browser/%s 115wangpan_android/%s", ver, ver, ver)
	payloadJSON, _ := json.Marshal(payload)
	form := url.Values{"data": {encrypt115(payloadJSON)}}
	body, err := post115Form("https://clouddownload.115.com/lixianssp/?ac=add_task_url", form, cookie, ua, 20*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "离线下载请求失败: " + err.Error()})
		return
	}

	// 解析响应
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析响应失败: " + truncateStr(string(body), 150)})
		return
	}
	if !resp.State {
		c.JSON(http.StatusBadGateway, gin.H{"error": "115 拒绝: " + resp.Error})
		return
	}

	log.Printf("[上传] ✓ 离线下载任务已提交: %s（%s）", truncateStr(req.URL, 60), linkType)
	c.JSON(http.StatusOK, gin.H{
		"message": "离线下载任务已提交，可在 115 客户端或网页版查看进度",
		"type":    linkType,
		"note":    "下载完成后由「自动整理+增量同步」接管",
	})
}

// offlineTaskList 查询离线下载任务列表
// GET /offline/tasks
func (h *Handler) offlineTaskList(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ver := getAppVerCached()
	ua := fmt.Sprintf("Mozilla/5.0 115disk/%s 115Browser/%s 115wangpan_android/%s", ver, ver, ver)
	payload, _ := json.Marshal(map[string]string{})
	form := url.Values{"data": {encrypt115(payload)}}
	body, err := post115Form("https://clouddownload.115.com/lixianssp/?ac=task_lists", form, cookie, ua, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询失败: " + err.Error()})
		return
	}

	var resp struct {
		State bool `json:"state"`
		Data  json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.State {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询被拒"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": json.RawMessage(resp.Data)})
}

// classifyLink 判断链接类型
func classifyLink(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "magnet:?"):
		return "magnet"
	case strings.HasPrefix(lower, "ed2k://"):
		return "ed2k"
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return "http"
	case strings.HasPrefix(lower, "ftp://"):
		return "ftp"
	case strings.Contains(lower, "115.com/s/"):
		return "share"
	default:
		return ""
	}
}
