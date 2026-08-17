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
		URL      string `json:"url"`
		Target   string `json:"target_cid"`
		Organize bool   `json:"organize"`
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

	// 用 web 版离线下载接口（Cookie 认证，表单提交）
	form := url.Values{"url": {req.URL}}
	if req.Target != "" {
		form.Set("wp_path_id", req.Target)
	}
	// 先尝试 115.com 的 web 离线下载接口
	body, err := post115Form("https://115.com/web/lixian/?ac=add_task_url", form, cookie, ua115Unified(), 20*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "离线下载请求失败: " + err.Error()})
		return
	}
	log.Printf("[上传] 离线下载响应: %s", truncateStr(string(body), 300))

	// 解析响应（兼容多种错误字段名）
	var resp struct {
		State    bool   `json:"state"`
		Error    string `json:"error"`
		ErrorMsg string `json:"error_msg"`
		ErrNo    int    `json:"errno"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errMsg"`
		Message string `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析响应失败: " + truncateStr(string(body), 300)})
		return
	}
	if !resp.State {
		// 按优先级取错误信息：error_msg > error > errMsg > message > 错误码
		errMsg := resp.ErrorMsg
		if errMsg == "" {
			errMsg = resp.Error
		}
		if errMsg == "" {
			errMsg = resp.ErrMsg
		}
		if errMsg == "" {
			errMsg = resp.Message
		}
		if errMsg == "" && resp.ErrNo != 0 {
			errMsg = fmt.Sprintf("错误码 %d", resp.ErrNo)
		}
		if errMsg == "" && resp.ErrCode != 0 {
			errMsg = fmt.Sprintf("错误码 %d", resp.ErrCode)
		}
		if errMsg == "" {
			errMsg = "未知原因（响应: " + truncateStr(string(body), 100) + "）"
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": errMsg})
		return
	}

	log.Printf("[上传] ✓ 离线下载任务已提交: %s（%s）", truncateStr(req.URL, 60), linkType)

	// 离线下载是异步的：提交后启动一个延迟检查协程，60 秒后触发增量
	// （如果 115 秒传命中则文件已就位；否则等下载完成后的下一轮 cron 接管）
	if req.Organize {
		go func() {
			time.Sleep(60 * time.Second)
			h.triggerOrganizeAndSync()
		}()
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "离线下载任务已提交，下载完成后自动生成 STRM",
		"type":    linkType,
		"note":    "秒传命中约 1 分钟后 STRM 生成；新下载需等 115 下载完成后由定时任务接管",
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

// triggerOrganizeAndSync 转存后自动「整理+增量同步」（整理优先，增量收尾）
func (h *Handler) triggerOrganizeAndSync() {
	time.Sleep(3 * time.Second)

	if !fullSyncMu.TryLock() {
		log.Printf("[上传] ○ 自动整理已跳过（已有任务运行中）")
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("自动整理+增量（转存触发）")
	defer endTask()

	start := time.Now()
	log.Printf("[上传] ▶ 转存后自动整理+增量同步开始...")

	// 整理：把转存目录加入待整理扫描范围
	// 转存目录（share.folder）可能 ≠ 待整理目录（org-basic.pending），需要都扫
	shareFolder := ""
	var shareCfg struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &shareCfg)
	shareFolder = shareCfg.Folder

	// 先用原始待整理目录整理
	_, _, orgErr := h.executeOrganize(false)
	if orgErr != nil {
		log.Printf("[上传] ○ 整理待整理目录跳过: %v", orgErr)
	}

	// 如果转存目录 ≠ 待整理目录，再用转存目录作为待整理目录整理一次
	if shareFolder != "" {
		orgCfg, err := h.loadOrgConfig()
		if err == nil && orgCfg.Pending != shareFolder {
			log.Printf("[上传] ▶ 转存目录 ≠ 待整理目录，额外扫描转存目录...")
			orgCfg.Pending = shareFolder
			h.executeOrganizeWithConfig(orgCfg, false)
		}
	}

	// 增量同步
	p := h.incrParamsFromConfig()
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		log.Printf("[上传] ✗ 自动增量同步失败: %v", err)
		return
	}
	log.Printf("[上传] ✅ 自动整理+增量完成，耗时 %s · STRM %d，附属 %d",
		time.Since(start).Truncate(time.Second), sum.StrmCreated, sum.AssetsDownloaded)
}

// triggerIncrementalAfterTransfer 转存/离线下载成功后立即触发增量同步
// （协程内执行，不阻塞 HTTP 响应；等 3 秒让 115 服务端写完文件索引）
func (h *Handler) triggerIncrementalAfterTransfer() {
	time.Sleep(3 * time.Second)

	if !fullSyncMu.TryLock() {
		log.Printf("[上传] ○ 增量同步已跳过（已有任务运行中）")
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("增量同步（转存触发）")
	defer endTask()

	p := h.incrParamsFromConfig()
	log.Printf("[上传] ▶ 转存/下载后自动增量同步开始...")
	start := time.Now()
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		log.Printf("[上传] ✗ 自动增量同步失败: %v", err)
		return
	}
	log.Printf("[上传] ✅ 自动增量同步完成，耗时 %s · STRM %d，附属 %d",
		time.Since(start).Truncate(time.Second), sum.StrmCreated, sum.AssetsDownloaded)
}
