package api

// ==================== 增量同步 Cron 调度器 ====================
//
// 支持标准 5 字段 cron（分 时 日 月 周），字段支持 * 、*/n 、a-b 、逗号列表。
// 每分钟检查一次，命中且无同步任务运行时触发一次增量同步（CMS lift_sync_task 模式）。

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"
)

// cronFieldMatch 检查单个 cron 字段是否匹配当前值
func cronFieldMatch(field string, v int) bool {
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// */n 或 *：任意值
		if part == "*" {
			return true
		}
		if strings.HasPrefix(part, "*/") {
			if n, err := strconv.Atoi(part[2:]); err == nil && n > 0 && v%n == 0 {
				return true
			}
			continue
		}
		// a-b 范围
		if i := strings.IndexByte(part, '-'); i > 0 {
			lo, err1 := strconv.Atoi(part[:i])
			hi, err2 := strconv.Atoi(part[i+1:])
			if err1 == nil && err2 == nil && v >= lo && v <= hi {
				return true
			}
			continue
		}
		// 单值
		if n, err := strconv.Atoi(part); err == nil && n == v {
			return true
		}
	}
	return false
}

// CronMatch 判断 5 字段 cron 表达式是否命中给定时间（分 时 日 月 周）
func CronMatch(expr string, t time.Time) bool {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return false
	}
	return cronFieldMatch(fields[0], t.Minute()) &&
		cronFieldMatch(fields[1], t.Hour()) &&
		cronFieldMatch(fields[2], t.Day()) &&
		cronFieldMatch(fields[3], int(t.Month())) &&
		cronFieldMatch(fields[4], int(t.Weekday()))
}

// loadIncrCron 从配置读取增量同步 cron（setting "incr" 的 cron 字段）
func (h *Handler) loadIncrCron() string {
	v := h.Config.GetSetting("incr")
	if v == "" {
		return ""
	}
	var cfg struct {
		Cron string `json:"cron"`
	}
	if json.Unmarshal([]byte(v), &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Cron)
}

// incrParamsFromConfig 从已保存的 full 配置组装增量参数
func (h *Handler) incrParamsFromConfig() incrParams {
	v := h.Config.GetSetting("full")
	p := incrParams{Cid: "0", LocalPath: "/media", Limit: 1000}
	if v != "" {
		var cfg struct {
			Cid       string   `json:"cid"`
			Path      string   `json:"path"`
			LocalPath string   `json:"local_path"`
			VideoExt  []string `json:"video_ext"`
			ImageExt  []string `json:"image_ext"`
			DataExt   []string `json:"data_ext"`
		}
		if json.Unmarshal([]byte(v), &cfg) == nil {
			if cfg.Cid != "" {
				p.Cid = cfg.Cid
			}
			if cfg.LocalPath != "" {
				p.LocalPath = cfg.LocalPath
			}
			p.VideoExt, p.ImageExt, p.DataExt = cfg.VideoExt, cfg.ImageExt, cfg.DataExt
		}
	}
	return p
}

// StartIncrScheduler 启动分钟级调度器：incr 配置的 cron 命中时自动执行
// 「自动整理 → 增量同步」流水线（CMS 主任务模式；全量同步仅供手动触发，不参与调度）
func StartIncrScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cron := h.loadIncrCron()
			if cron == "" {
				continue // 未配置调度
			}
			if !CronMatch(cron, time.Now()) {
				continue
			}
			if !fullSyncMu.TryLock() {
				log.Printf("[调度] cron 触发但已有任务运行，跳过本轮")
				continue
			}
			log.Printf("[调度] 整理+增量 开始（cron: %s）", cron)
			beginTask("定时整理+增量")
			start := time.Now()
			// 1) 自动整理（不联动全量同步，交给下一步增量精确处理）
			orgSteps, _, orgErr := h.executeOrganize(false)
			if orgErr != nil {
				log.Printf("[调度] 自动整理跳过: %v", orgErr)
			} else {
				for _, st := range orgSteps {
					msg, _ := st["message"].(string)
					log.Printf("[调度] 整理: %v", msg)
				}
			}
			// 2) 增量同步（整理产生的 move 事件会被精确应用）
			p := h.incrParamsFromConfig()
			sum, err := h.executeIncrementalSync(p)
			if err != nil {
				log.Printf("[调度] 增量同步失败: %v", err)
			} else if sum != nil {
				log.Printf("[调度] 增量: 新事件 %d，删 %d，移/改 %d，STRM %d，附属下载 %d",
					sum.EventsFresh, sum.Deleted, sum.Moved, sum.StrmCreated, sum.AssetsDownloaded)
			}
			log.Printf("[调度] 整理+增量 完成, time = %.2fs", time.Since(start).Seconds())
			endTask()
			fullSyncMu.Unlock()
		}
	}()
	log.Println("[调度] 调度器已启动（cron 触发 自动整理+增量同步；全量同步仅手动）")
}
