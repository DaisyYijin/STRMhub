package api

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"gorm.io/gorm"
)

// ==================== 115 全局请求节流器 ====================
//
// 115 对高频请求有风控（返回"服务器开小差了"），且被风控后会持续一段时间。
// 所有对 webapi.115.com / proapi.115.com 的请求必须经过 throttle115()，
// 保证任意两次请求之间的最小间隔。
//
// 可通过环境变量 STRMHUB_115_INTERVAL（毫秒）调整，默认 1000ms。
// 登录相关域名（qrcodeapi / passportapi）不节流，不影响扫码体验。

var (
	throttleMu     sync.Mutex
	throttleLast   time.Time
	throttleMinGap = loadThrottleInterval()

	lastWaitMu sync.Mutex
	lastWait   time.Duration // 最近一次节流等待时长（供同步日志显示）
)

// loadThrottleInterval 读取节流间隔配置
func loadThrottleInterval() time.Duration {
	ms := 1000
	if v := os.Getenv("STRMHUB_115_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 100 && n <= 60000 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// isThrottledHost 判断是否为需要节流的 115 域名（文件操作类接口，含镜像域名）
func isThrottledHost(api string) bool {
	return strings.Contains(api, "webapi.115.com") ||
		strings.Contains(api, "web.api.115.com") ||
		strings.Contains(api, "pro.api.115.com") ||
		strings.Contains(api, "115cdn.com") ||
		strings.Contains(api, "115vod.com") ||
		strings.Contains(api, "proapi.115.com")
}

// throttle115 在发起 115 文件类 API 请求前调用，确保与上一请求【完成时刻】
// 的间隔不小于设置值（完成一个之后等待 N 秒再发下一个）
func throttle115(api string) {
	if !isThrottledHost(api) {
		return
	}
	throttleMu.Lock()
	var waited time.Duration
	if elapsed := time.Since(throttleLast); elapsed < throttleMinGap {
		waited = throttleMinGap - elapsed
		// 明显拥堵（排队超过 3 个间隔）才提示
		if waited > 3*throttleMinGap {
			log.Printf("[115节流] 等待 %v 后再请求 %s", waited.Truncate(time.Millisecond), api)
		}
		time.Sleep(waited)
	}
	throttleMu.Unlock()

	lastWaitMu.Lock()
	lastWait = waited
	lastWaitMu.Unlock()
}

// throttle115Done 请求完成后调用，推进节流锚点（锚点=完成时刻）
func throttle115Done(api string) {
	if !isThrottledHost(api) {
		return
	}
	throttleMu.Lock()
	throttleLast = time.Now()
	throttleMu.Unlock()
}

// throttle115LastWait 返回最近一次节流的等待时长（同步日志展示用）
func throttle115LastWait() time.Duration {
	lastWaitMu.Lock()
	defer lastWaitMu.Unlock()
	return lastWait
}

// Set115Interval 运行时调整节流间隔（供设置接口调用）
func Set115Interval(d time.Duration) {
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	throttleMu.Lock()
	throttleMinGap = d
	throttleMu.Unlock()
}

// Apply115Interval 从数据库读取用户设置的 API 请求间隔并应用
// 优先级：数据库设置 > STRMHUB_115_INTERVAL 环境变量 > 默认 1 秒
func Apply115Interval(db *gorm.DB) {
	var storage model.Storage
	if err := db.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Interval <= 0 {
		return
	}
	Set115Interval(time.Duration(storage.Interval * float64(time.Second)))
	log.Printf("[115节流] 已应用用户设置的 API 请求间隔: %v", time.Duration(storage.Interval*float64(time.Second)))
}
