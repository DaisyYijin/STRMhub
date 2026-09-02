package api

// TG 关键词订阅（基于 TG 频道搜索通道）：
// 订阅若干「关键词（可选指定频道）」，调度器按间隔轮询频道公开预览，
// 用消息 ID 做水位去重，命中新资源时推送通知；
// 开启「自动转存」时，命中的 115 分享/磁力直接入库（与 TG 搜索的转存同链路）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type TgSubItem struct {
	ID       int64  `json:"id"`
	Keyword  string `json:"keyword"`
	Channels string `json:"channels"` // 空 = 用 TG 搜索的全局频道
	Auto     bool   `json:"auto"`     // 命中后自动转存/离线
	LastID   int64  `json:"last_id"`  // 水位：已见过的最大消息 ID（0 = 新订阅，首轮只建水位）
	LastHit  string `json:"last_hit"` // 最近一次命中时间
}

type tgSubCfg struct {
	Items       []TgSubItem `json:"items"`
	IntervalMin int         `json:"interval_min"` // 检查间隔（分钟），最小 5，默认 30
}

func (h *Handler) loadTgSubCfg() tgSubCfg {
	c := tgSubCfg{IntervalMin: 30}
	if v := h.Config.GetSetting("tgsub"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (h *Handler) saveTgSubCfg(c tgSubCfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("tgsub", string(b))
}

// ==================== 检查 ====================

var tgSubRunning sync.Mutex

// tgSubCheck 单轮检查全部订阅。LastID=0 的新订阅首轮只建水位不通知（防历史消息轰炸）
func (h *Handler) tgSubCheck(silent bool) {
	if !tgSubRunning.TryLock() {
		return
	}
	defer tgSubRunning.Unlock()
	cfg := h.loadTgSubCfg()
	if len(cfg.Items) == 0 {
		return
	}
	changed := false
	for i := range cfg.Items {
		item := &cfg.Items[i]
		channels := item.Channels
		if strings.TrimSpace(channels) == "" {
			channels = h.loadTgSearchCfg().Channels // 默认用 TG 搜索的全局频道
		}
		firstRun := item.LastID == 0
		var maxID int64
		var hits []tgItem
		for _, ch := range strings.Split(channels, "\n") {
			ch = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ch), "@"))
			if ch == "" {
				continue
			}
			items, err := tgSearchChannel(ch, item.Keyword)
			if err != nil {
				continue
			}
			for _, it := range items {
				if it.MsgID > maxID {
					maxID = it.MsgID
				}
				if it.MsgID > item.LastID {
					hits = append(hits, it)
				}
			}
			time.Sleep(800 * time.Millisecond) // 频道间节流
		}
		if maxID > item.LastID {
			item.LastID = maxID
			changed = true
		}
		if silent || firstRun || len(hits) == 0 {
			continue
		}
		item.LastHit = time.Now().Format("01-02 15:04")
		changed = true
		lines := []string{fmt.Sprintf("🔔 订阅命中「%s」：%d 条新资源", item.Keyword, len(hits))}
		for k, it := range hits {
			if k >= 5 {
				lines = append(lines, fmt.Sprintf("…等共 %d 条", len(hits)))
				break
			}
			link := ""
			if len(it.Links) > 0 {
				link = it.Links[0].URL
			}
			lines = append(lines, fmt.Sprintf("• %s\n  %s", truncateStr(it.Title, 60), truncateStr(link, 90)))
			if item.Auto && link != "" {
				h.tgSubAutoSave(link, it.Pass)
			}
		}
		NotifyMessage("", strings.Join(lines, "\n"))
	}
	if changed {
		h.saveTgSubCfg(cfg)
	}
}

// tgSubAutoSave 自动入库：115 分享走转存，磁力/ed2k 提交离线下载
func (h *Handler) tgSubAutoSave(link, pass string) {
	if is115ShareLink(link) {
		msg, ok, fail, err := h.shareReceiveCore(link, pass, h.shareFolderCid(), true)
		if err != nil {
			log.Printf("[TG订阅] ✗ 自动转存失败: %v", err)
			return
		}
		NotifyMessage("", fmt.Sprintf("🔔 订阅自动转存完成（%d 成功/%d 失败）\n%s", ok, fail, truncateStr(msg, 80)))
		return
	}
	if strings.HasPrefix(link, "magnet:") || strings.HasPrefix(link, "ed2k:") {
		if err := h.submitOfflineLink(link); err != nil {
			log.Printf("[TG订阅] ✗ 自动离线提交失败: %v", err)
			return
		}
		NotifyMessage("", "🔔 订阅自动离线下载已提交")
	}
}

// ==================== 调度与处理器 ====================

var tgSubLastRun time.Time

func StartTgSubScheduler(h *Handler) {
	go func() {
		time.Sleep(40 * time.Second)
		h.tgSubCheck(true) // 首轮只建水位
		tgSubLastRun = time.Now()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			cfg := h.loadTgSubCfg()
			interval := cfg.IntervalMin
			if interval < 5 {
				interval = 5
			}
			if time.Since(tgSubLastRun) < time.Duration(interval)*time.Minute {
				continue
			}
			tgSubLastRun = time.Now()
			go h.tgSubCheck(false)
		}
	}()
	log.Println("[TG订阅] 调度器已启动")
}

// TgSubGetConfig GET /tgsub/config
func (h *Handler) TgSubGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": h.loadTgSubCfg()})
}

// TgSubSaveConfig POST /tgsub/config（前端整表提交：新增/修改/删除）
func (h *Handler) TgSubSaveConfig(c *gin.Context) {
	var req tgSubCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.IntervalMin < 5 {
		req.IntervalMin = 5
	}
	// 新条目分配 ID（LastID 保持 0 → 下轮只建水位，不轰炸历史）
	var maxID int64 = 1
	for _, it := range req.Items {
		if it.ID > maxID {
			maxID = it.ID
		}
	}
	for i := range req.Items {
		if req.Items[i].ID == 0 {
			maxID++
			req.Items[i].ID = maxID
		}
	}
	h.saveTgSubCfg(req)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// TgSubRun POST /tgsub/run：立即检查一轮（新消息会通知）
func (h *Handler) TgSubRun(c *gin.Context) {
	tgSubLastRun = time.Now()
	go h.tgSubCheck(false)
	c.JSON(http.StatusOK, gin.H{"message": "检查已开始，命中会推送通知"})
}
