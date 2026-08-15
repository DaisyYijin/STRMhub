package api

// ==================== 仪表盘 + 代理测试 ====================

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// TestProxyLatency 测试代理延迟：通过代理访问 Google，返回毫秒
// POST /proxy/test
func (h *Handler) TestProxyLatency(c *gin.Context) {
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写代理地址"})
		return
	}

	proxyURL, err := url.Parse(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "代理地址格式错误"})
		return
	}

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	start := time.Now()
	resp, err := client.Get("https://www.google.com/generate_204")
	elapsed := time.Since(start)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "latency_ms": -1})
		return
	}
	resp.Body.Close()
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"latency_ms": elapsed.Milliseconds(),
		"target":     "google_204",
	})
}

// DashboardEnhanced 仪表盘（真实数据版）
// GET /dashboard
func (h *Handler) DashboardEnhanced(c *gin.Context) {
	// strm 统计
	var strmTotal, strmInvalid, syncedFiles int64
	h.DB.Model(&model.StrmFile{}).Count(&strmTotal)
	h.DB.Model(&model.StrmFile{}).Where("status = ?", "invalid").Count(&strmInvalid)
	h.DB.Model(&model.SyncedFile{}).Count(&syncedFiles)

	// 整理记录
	var organizedTotal int64
	h.DB.Model(&model.MediaLibrary{}).Count(&organizedTotal)

	// 最近整理入库（前 10 部）
	var recentMedia []model.MediaLibrary
	h.DB.Order("created_at DESC").Limit(10).Find(&recentMedia)
	recent := make([]gin.H, 0, len(recentMedia))
	for _, m := range recentMedia {
		recent = append(recent, gin.H{
			"title": m.Title, "year": m.Year, "category": m.Category,
			"type": m.MediaType, "path": m.TargetPath, "at": m.CreatedAt.Format("01-02 15:04"),
		})
	}

	// 最近同步事件（未处理的）
	var pendingEvents int64
	h.DB.Model(&model.SyncEvent{}).Where("status = ?", "pending").Count(&pendingEvents)

	// 运行时间（进程启动时间）
	c.JSON(http.StatusOK, gin.H{
		"strm": gin.H{
			"total":   strmTotal,
			"invalid": strmInvalid,
			"active":  strmTotal - strmInvalid,
		},
		"synced_files": syncedFiles,
		"organized":    organizedTotal,
		"recent_media": recent,
		"pending_events": pendingEvents,
		"pan115":        h.pan115CapacityCached(),
		"task_running":  func() bool { r, _, _ := TaskStatus(); return r }(),
	})
}

// saveProxyConfigToDB 把代理配置写入 DB（TMDB/GPT 请求共用）
func saveProxyConfigToDB(h *Handler, proxyURL string) {
	var s model.Setting
	if err := h.DB.Where("`key` = ?", "proxy").First(&s).Error; err == nil {
		h.DB.Model(&s).Update("value", fmt.Sprintf(`{"url":%q}`, proxyURL))
	} else {
		h.DB.Create(&model.Setting{Key: "proxy", Value: fmt.Sprintf(`{"url":%q}`, proxyURL)})
	}
}

