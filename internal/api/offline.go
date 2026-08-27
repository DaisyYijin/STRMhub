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
	vlog("[上传] 离线下载响应: %s", truncateStr(string(body), 150))

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

	// 离线下载是异步的：提交后 10 秒先试探一轮（115 秒传命中时文件已就位，
	// CMS 同款极速响应——秒传场景 ~15 秒即开始整理）；未命中则 60 秒后再试，
	// 仍没好就交给守望者/离线任务监视器（下载完成时触发）
	if req.Organize {
		go func() {
			time.Sleep(10 * time.Second)
			if cid := h.shareFolderCid(); cid != "" {
				if ops, err := h.newPan115Ops(); err == nil {
					if entries, _, lerr := ops.listEntries(cid, 0); lerr == nil && len(entries) > 0 {
						log.Printf("[上传] ⚡ 秒传命中（转存目录已有 %d 个条目），立即整理", len(entries))
						h.triggerOrganizeAndSync()
						return
					}
				}
			}
			time.Sleep(50 * time.Second)
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

	raws, err := fetchLixianTasksRaw(cookie)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询失败: " + err.Error()})
		return
	}
	// 规范化：name / size / percent / status(-1失败 1下载中 2完成)
	items := make([]gin.H, 0, len(raws))
	for _, m := range raws {
		name := firstStr(m, "name", "task_name")
		if name == "" {
			continue
		}
		status := 0
		switch v := m["status"].(type) {
		case float64:
			status = int(v)
		case string:
			switch {
			case strings.Contains(strings.ToLower(v), "fail") || strings.Contains(v, "失败"):
				status = -1
			case strings.Contains(v, "完成") || strings.Contains(strings.ToLower(v), "done"):
				status = 2
			case strings.Contains(v, "下载"):
				status = 1
			}
		}
		items = append(items, gin.H{"name": name, "size": m["size"], "percent": m["percent"], "status": status})
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// classifyLink 判断链接类型
func classifyLink(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "magnet:?"):
		return "magnet"
	case strings.HasPrefix(lower, "ed2k://"):
		return "ed2k"
	case strings.Contains(lower, "115.com/s/"):
		// 分享链接需先于 http 判断（分享链接也是 http(s) 开头）
		return "share"
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return "http"
	case strings.HasPrefix(lower, "ftp://"):
		return "ftp"
	default:
		return ""
	}
}

// triggerOrganizeAndSync 转存后自动「整理+增量同步」（整理优先，增量收尾）。
// 返回是否真正执行（false = 因互斥锁被其他任务占用而跳过）
func (h *Handler) triggerOrganizeAndSync() bool {
	time.Sleep(3 * time.Second)

	if !fullSyncMu.TryLock() {
		log.Printf("[上传] ○ 自动整理已跳过（已有任务运行中）")
		return false
	}
	defer fullSyncMu.Unlock()
	beginTask("自动整理+增量（转存触发）")
	defer endTask()

	start := time.Now()
	log.Printf("[上传] ▶ 转存后自动整理+增量同步开始...")

	// 整理：直接扫描转存目录（转存触发时不需要扫待整理目录）
	shareFolder := ""
	var shareCfg struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &shareCfg)
	shareFolder = shareCfg.Folder

	if shareFolder != "" {
		orgCfg, err := h.loadOrgConfig()
		if err != nil {
			log.Printf("[上传] ○ 整理跳过（配置缺失）: %v", err)
		} else if h.dirOverlapWithLibrary(shareFolder, orgCfg.Library) {
			// 转存目录与媒体库存在包含关系（转存进了库里，或转存目录覆盖整个库）：
			// 扫描会波及库内容，直接放弃本次自动整理（引擎层守卫之外的第二道防线）
			log.Printf("[上传] ⚠ 转存目录与媒体库存在包含关系，跳过自动整理以防误搬库内容，请到「分享同步」修正转存目录")
		} else {
			// 转存目录作为待整理目录
			if orgCfg.Pending != shareFolder {
				orgCfg.Pending = shareFolder
			}
			log.Printf("[上传] ▶ 直接扫描转存目录...")
			_, _, orgErr := h.executeOrganizeWithConfig(orgCfg, false)
			if orgErr != nil {
				log.Printf("[上传] ○ 转存目录整理失败: %v", orgErr)
			}
		}
	} else {
		// 没配转存目录，退回到扫待整理目录
		_, _, orgErr := h.executeOrganize(false)
		if orgErr != nil {
			log.Printf("[上传] ○ 整理跳过: %v", orgErr)
		}
	}

	// 增量同步
	p := h.incrParamsFromConfig()
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		log.Printf("[上传] ✗ 自动增量同步失败: %v", err)
		return true
	}
	log.Printf("[上传] ✅ 自动整理+增量完成，耗时 %s · STRM %d，附属 %d",
		time.Since(start).Truncate(time.Second), sum.StrmCreated, sum.AssetsDownloaded)
	return true
}

// StartTransferWatcher 转存目录守望者：每分钟检查转存目录，发现内容且
// 无任务运行时触发「自动整理+增量」。磁力/离线下载完成时间不可控，
// 提交 60 秒后的触发器常在下载完成前跑掉，定时任务又要等下一轮 cron——
// 守望者保证下载完成后约 1 分钟内被接管。5 分钟冷却防止整理失败时死循环
func StartTransferWatcher(h *Handler) {
	go func() {
		lastTrigger := time.Time{}
		failCount := 0    // 整理后目录仍未清空的连续次数
		pauseUntil := time.Time{}
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(60 * time.Second):
			}
			if time.Now().Before(pauseUntil) {
				continue // 熔断暂停中
			}
			if time.Since(lastTrigger) < 5*time.Minute {
				continue
			}
			cid := h.shareFolderCid()
			if cid == "" {
				continue
			}
			ops, err := h.newPan115Ops()
			if err != nil {
				continue
			}
			entries, _, err := ops.listEntries(cid, 0)
			if err != nil || len(entries) == 0 {
				continue
			}
			// 有内容：触发整理（内部自带互斥、与媒体库重叠校验、3 秒沉淀）
			log.Printf("[守望] ⚑ 转存目录有 %d 个条目，触发自动整理+增量", len(entries))
			ran := h.triggerOrganizeAndSync()
			// 成功清空后冷却只要 60 秒（连续多个下载先后完成时快速接续）；
			// 失败仍 5 分钟（在下方复查处设置）
			lastTrigger = time.Now().Add(4 * time.Minute)
			if !ran {
				lastTrigger = time.Now()
				continue // 其他任务占用，本轮不计成败
			}
			// 整理后复查：目录清空 = 成功；仍有条目 = 一轮失败。
			// 连续 3 次失败（如整理反复报错/内容无法处理）后暂停 30 分钟，
			// 避免每 5 分钟无限重试刷日志
			if remaining, _, rerr := ops.listEntries(cid, 0); rerr == nil && len(remaining) == 0 {
				failCount = 0
			} else {
				lastTrigger = time.Now() // 失败：恢复 5 分钟冷却
				failCount++
				log.Printf("[守望] ○ 整理后转存目录仍有内容（第 %d 次未清空）", failCount)
				if failCount >= 3 {
					pauseUntil = time.Now().Add(30 * time.Minute)
					log.Printf("[守望] ⚠ 连续 %d 次未能清空转存目录，暂停守望 30 分钟（请查看整理日志定位失败原因）", failCount)
					failCount = 0
				}
			}
		}
	}()
}

// StartOfflineTaskMonitor 离线任务监视器：30 秒轮询 115 离线任务列表。
// 磁力下载不是百分百成功（资源失效/任务报错都常见），且完成时间不可控：
//   - 任务完成（status=2）→ 立即触发整理（不等 60 秒目录轮询）
//   - 任务失败（status=-1）→ 日志告警（此前静默失败，用户永远等不到）
// 状态码语义与 LitePan/openapi 一致：-1 失败 / 0 排队 / 1 下载中 / 2 完成
func StartOfflineTaskMonitor(h *Handler) {
	go func() {
		lastStatus := map[string]int{} // info_hash/url → 上次状态
		notified := map[string]bool{}  // 已处理过的终态任务（避免重复告警/触发）
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(30 * time.Second):
			}
			cookie, err := h.get115Cookie()
			if err != nil {
				continue
			}
			tasks, err := fetchOfflineTaskList(cookie)
			if err != nil {
				continue // 网络抖动/接口拒绝：下轮再看
			}
			for _, t := range tasks {
				key := t.key
				prev, seen := lastStatus[key]
				lastStatus[key] = t.status
				if !seen || prev == t.status || notified[key] {
					continue
				}
				switch t.status {
				case 2: // 完成
					log.Printf("[离线] ✓ 下载完成: %s（触发整理）", truncateStr(t.name, 60))
					notified[key] = true
					go h.triggerOrganizeAndSync()
				case -1: // 失败
					log.Printf("[离线] ✗ 磁力下载失败: %s（115 离线任务报错，请检查资源或重新提交）", truncateStr(t.name, 60))
					go NotifyMessage("❌ 磁力下载失败", truncateStr(t.name, 120)+"\n（115 离线任务报错，请检查资源或重新提交）")
					notified[key] = true
				}
			}
			// 终态表防膨胀：只保留最近一轮见到的任务
			if len(notified) > 500 {
				notified = map[string]bool{}
			}
			if len(lastStatus) > 1000 {
				lastStatus = map[string]int{}
			}
		}
	}()
}

// fetchLixianTasksRaw 拉取离线任务原始列表（明文 web 接口，与 add_task_url 同通道；
// 兼容 data/info、数组、{tasks}/{list} 多种响应形态）
func fetchLixianTasksRaw(cookie string) ([]map[string]interface{}, error) {
	body, err := post115Form("https://115.com/web/lixian/?ac=task_lists", url.Values{}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return nil, err
	}
	var resp struct {
		State bool            `json:"state"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
		Info  json.RawMessage `json:"info"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.State {
		msg := resp.Error
		if msg == "" {
			msg = truncateStr(string(body), 120)
		}
		return nil, fmt.Errorf("115 拒绝: %s", msg)
	}
	raw := resp.Data
	if len(raw) == 0 {
		raw = resp.Info
	}
	var raws []map[string]interface{}
	if json.Unmarshal(raw, &raws) != nil {
		var wrapper struct {
			Tasks []map[string]interface{} `json:"tasks"`
			List  []map[string]interface{} `json:"list"`
		}
		if json.Unmarshal(raw, &wrapper) != nil {
			return nil, fmt.Errorf("任务列表结构无法解析: %s", truncateStr(string(raw), 100))
		}
		raws = append(wrapper.Tasks, wrapper.List...)
	}
	return raws, nil
}

// offlineTaskInfo 离线任务摘要
type offlineTaskInfo struct {
	key    string // info_hash 优先，空则 name
	name   string
	status int
}

// fetchOfflineTaskList 拉取离线任务列表（web lixian 加密接口，防御式解析）
func fetchOfflineTaskList(cookie string) ([]offlineTaskInfo, error) {
	raws, err := fetchLixianTasksRaw(cookie)
	if err != nil {
		return nil, err
	}
	tasks := make([]offlineTaskInfo, 0, len(raws))
	for _, m := range raws {
		name := firstStr(m, "name", "task_name")
		if name == "" {
			continue
		}
		status := 0
		switch v := m["status"].(type) {
		case float64:
			status = int(v)
		case string:
			switch {
			case strings.Contains(strings.ToLower(v), "fail") || strings.Contains(v, "失败"):
				status = -1
			case strings.Contains(v, "完成") || strings.Contains(strings.ToLower(v), "done"):
				status = 2
			case strings.Contains(v, "下载"):
				status = 1
			}
		}
		key := firstStr(m, "info_hash", "infoHash")
		if key == "" {
			key = name
		}
		tasks = append(tasks, offlineTaskInfo{key: key, name: name, status: status})
	}
	return tasks, nil
}

// shareFolderCid 读取分享同步配置的转存目录 cid（未配置返回空）
func (h *Handler) shareFolderCid() string {
	var cfg struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &cfg)
	return cfg.Folder
}

// dirInside 判断 childCid 是否位于 parentCid 子树内（含相等），取不到路径返回 false
func (h *Handler) dirInside(childCid, parentCid string) bool {
	if childCid == "" || parentCid == "" || childCid == parentCid {
		return childCid == parentCid && childCid != ""
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return false
	}
	memo := map[string]dirInfo{}
	childAbs := strings.TrimSuffix(absPathOf(cookie, childCid, memo), "/")
	parentAbs := strings.TrimSuffix(absPathOf(cookie, parentCid, memo), "/")
	if childAbs == "" || parentAbs == "" {
		return false
	}
	return childAbs == parentAbs || strings.HasPrefix(childAbs+"/", parentAbs+"/")
}

// dirOverlapWithLibrary 判断转存目录与媒体库是否存在包含关系
// （相等、转存目录在库内、或转存目录覆盖整个库都算）。取不到路径时返回
// false 放行，由引擎层的 orgGuards 兜底
func (h *Handler) dirOverlapWithLibrary(shareCid, libCid string) bool {
	if shareCid == "" || libCid == "" {
		return false
	}
	if shareCid == libCid {
		return true
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return false
	}
	memo := map[string]dirInfo{}
	shareAbs := strings.TrimSuffix(absPathOf(cookie, shareCid, memo), "/")
	libAbs := strings.TrimSuffix(absPathOf(cookie, libCid, memo), "/")
	if shareAbs == "" || libAbs == "" {
		return false
	}
	return shareAbs == libAbs ||
		strings.HasPrefix(shareAbs+"/", libAbs+"/") || // 转存目录在媒体库内
		strings.HasPrefix(libAbs+"/", shareAbs+"/") // 转存目录覆盖媒体库
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
