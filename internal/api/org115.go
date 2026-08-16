package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

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

