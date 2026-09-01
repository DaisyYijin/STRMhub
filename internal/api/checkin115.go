package api

// ==================== 115 每日签到 ====================
//
// 接口（p115client user_points_sign 同款）：
//   GET  https://proapi.115.com/android/2.0/user/points_sign  查今日签到状态
//   POST https://proapi.115.com/android/2.0/user/points_sign  签到
//        token = sha1("{user_id}-Points_Sign@#115-{t}")，token_time = t
//   奖励：每日签到得积分（连续签到有加成），入口在 115 App「积分中心」。
//   注意：该接口按 Cookie 通道走（与全站同款统一 UA，保持会话一致性）。
//
// 调度：每日在配置的时间窗口（默认 06:00-09:00）内随机一个时刻执行，
// 失败自动在剩余窗口内重试；结果推送企微/TG 通知。对齐 MoviePilot
// p115strmhelper 的 p115_checkin 调度策略。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// randIntN [0,n) 随机整数（n<=0 返回 0）
func randIntN(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}

const sign115API = "https://proapi.115.com/android/2.0/user/points_sign"

// 签到配置（setting key "115checkin"）
type checkin115Cfg struct {
	Enabled      bool   `json:"enabled"`
	TimeRange    string `json:"time_range"`    // HH:MM-HH:MM 随机窗口
	LastDone     string `json:"last_done"`     // 最近签到成功的日期 yyyy-mm-dd
	NextRun      int64  `json:"next_run"`      // 下次执行时刻（unix 秒）
	LastResult   string `json:"last_result"`   // 最近一次执行结果文案
	LastResultAt string `json:"last_result_at"` // 最近一次执行时间
}

var (
	checkin115Mu sync.Mutex
	checkin115V  *checkin115Cfg
	checkin115At time.Time
	checkinRunMu sync.Mutex
)

func loadCheckin115Cfg() *checkin115Cfg {
	checkin115Mu.Lock()
	defer checkin115Mu.Unlock()
	if checkin115V != nil && time.Since(checkin115At) < 10*time.Second {
		return checkin115V
	}
	cfg := &checkin115Cfg{TimeRange: "06:00-09:00"}
	if v := settingValueCompat("115checkin"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	if cfg.TimeRange == "" {
		cfg.TimeRange = "06:00-09:00"
	}
	checkin115V = cfg
	checkin115At = time.Now()
	return cfg
}

func saveCheckin115Cfg(cfg *checkin115Cfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("115checkin", string(b)); err != nil {
		return err
	}
	checkin115Mu.Lock()
	checkin115V = cfg
	checkin115At = time.Now()
	checkin115Mu.Unlock()
	return nil
}

var reCheckinWindow = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$`)

// parseCheckinWindow 解析 HH:MM-HH:MM 为当日时、分（非法回落 06:00-09:00）
func parseCheckinWindow(s string) (h1, m1, h2, m2 int) {
	h1, m1, h2, m2 = 6, 0, 9, 0
	m := reCheckinWindow.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return
	}
	fmt.Sscanf(m[1], "%d", &h1)
	fmt.Sscanf(m[2], "%d", &m1)
	fmt.Sscanf(m[3], "%d", &h2)
	fmt.Sscanf(m[4], "%d", &m2)
	return
}

// checkinRandEpoch 在指定日期的窗口内随机一个时刻（unix 秒）
func checkinRandEpoch(d time.Time, h1, m1, h2, m2 int) int64 {
	start := time.Date(d.Year(), d.Month(), d.Day(), h1, m1, 0, 0, time.Local)
	end := time.Date(d.Year(), d.Month(), d.Day(), h2, m2, 0, 0, time.Local)
	if !end.After(start) {
		end = start.Add(30 * time.Minute)
	}
	span := end.Unix() - start.Unix()
	return start.Unix() + int64(randIntN(int(span+1)))
}

// pickCheckinNextRun 依据当前时刻选下次执行点：当日窗口未结束 → 剩余窗口内
// 随机；否则明天窗口内随机
func pickCheckinNextRun(now time.Time, timeRange string) int64 {
	h1, m1, h2, m2 := parseCheckinWindow(timeRange)
	if s := time.Date(now.Year(), now.Month(), now.Day(), h1, m1, 0, 0, time.Local); now.Before(s) {
		return checkinRandEpoch(now, h1, m1, h2, m2)
	}
	if e := time.Date(now.Year(), now.Month(), now.Day(), h2, m2, 0, 0, time.Local); now.Before(e) {
		start := time.Date(now.Year(), now.Month(), now.Day(), h1, m1, 0, 0, time.Local)
		if start.Before(now) {
			start = now
		}
		end := e
		span := int(end.Unix()-start.Unix()) + 1
		if span < 1 {
			span = 1
		}
		return start.Unix() + int64(randIntN(span))
	}
	tomorrow := now.AddDate(0, 0, 1)
	return checkinRandEpoch(tomorrow, h1, m1, h2, m2)
}

// sign115Status 查询今日签到状态（is_sign_today: 1=已签）
func sign115Status(cookie string) (signed bool, err error) {
	body, err := httpGet115UA(sign115API, nil, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return false, err
	}
	var out struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			IsSignToday int `json:"is_sign_today"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil {
		return false, fmt.Errorf("响应解析失败: %s", truncateStr(string(body), 100))
	}
	if !out.State {
		return false, fmt.Errorf("%s", out.Error)
	}
	return out.Data.IsSignToday == 1, nil
}

// run115CheckinOnce 执行一次签到：查状态 → 签到（重试 3 次，间隔 3 秒）。
// notify=true 时把结果推送企微/TG（定时触发用；手动触发走界面返回）。
func (h *Handler) run115CheckinOnce(notify bool) (bool, string) {
	checkinRunMu.Lock()
	defer checkinRunMu.Unlock()

	cookie, err := h.get115Cookie()
	if err != nil || cookie == "" {
		msg := "未绑定 115 账号，无法签到"
		log.Printf("[115签到] ✗ %s", msg)
		return false, msg
	}

	// 1. 今日是否已签
	if signed, err := sign115Status(cookie); err == nil && signed {
		msg := "今日已签到，无需重复签到"
		log.Printf("[115签到] ○ %s", msg)
		h.saveCheckinResult(true, msg)
		return true, msg
	}

	// 2. 执行签到（带 token 防重放，失败重试）
	uid := cookieUserID(cookie)
	ok := false
	var msg string
	for attempt := 1; attempt <= 3; attempt++ {
		t := time.Now().Unix()
		sum := sha1.Sum([]byte(fmt.Sprintf("%d-Points_Sign@#115-%d", uid, t)))
		rbody, err := httpPostForm115(sign115API, url.Values{
			"token":      {hex.EncodeToString(sum[:])},
			"token_time": {fmt.Sprint(t)},
		}, cookie, 15*time.Second)
		if err != nil {
			msg = "请求失败: " + err.Error()
		} else {
			var r struct {
				State bool   `json:"state"`
				Error string `json:"error"`
				Data  struct {
					ContinuousDay int `json:"continuous_day"`
					PointsNum     int `json:"points_num"`
				} `json:"data"`
			}
			if json.Unmarshal(rbody, &r) == nil {
				switch {
				case r.State:
					ok = true
					msg = fmt.Sprintf("签到成功，连续签到 %d 天，获得 %d 积分", r.Data.ContinuousDay, r.Data.PointsNum)
				case strings.Contains(r.Error, "已签到"):
					ok = true
					msg = "今日已签到，无需重复签到"
				default:
					msg = r.Error
				}
			} else {
				msg = "响应解析失败: " + truncateStr(string(rbody), 100)
			}
		}
		if ok {
			break
		}
		if attempt < 3 {
			log.Printf("[115签到] ○ 第 %d/3 次失败: %s，3 秒后重试", attempt, msg)
			time.Sleep(3 * time.Second)
		}
	}

	if ok {
		log.Printf("[115签到] ✓ %s", msg)
	} else {
		log.Printf("[115签到] ✗ %s", msg)
	}
	h.saveCheckinResult(ok, msg)
	if notify {
		title := "115 签到失败"
		if ok {
			title = "115 签到成功"
		}
		NotifyMessage(title, msg)
	}
	return ok, msg
}

func (h *Handler) saveCheckinResult(ok bool, msg string) {
	cfg := loadCheckin115Cfg()
	cfg.LastResult = msg
	cfg.LastResultAt = time.Now().Format("01-02 15:04")
	if ok {
		cfg.LastDone = time.Now().Format("2006-01-02")
	}
	_ = saveCheckin115Cfg(cfg)
}

// ==================== 调度器 ====================

// Start115CheckinScheduler 分钟级 tick：到点执行每日签到（窗口内随机时刻）
func Start115CheckinScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.checkinTick()
		}
	}()
	log.Println("[调度] 115 签到调度器已启动（每日时间窗口内随机执行）")
}

func (h *Handler) checkinTick() {
	cfg := loadCheckin115Cfg()
	if !cfg.Enabled {
		return
	}
	now := time.Now()
	today := now.Format("2006-01-02")

	if cfg.LastDone == today {
		// 今天已签：确保下次执行点已安排在明天窗口
		if cfg.NextRun > now.Unix() {
			nr := time.Unix(cfg.NextRun, 0)
			if nr.Format("2006-01-02") != today {
				return
			}
		}
		cfg.NextRun = pickCheckinNextRun(now.AddDate(0, 0, 1), cfg.TimeRange)
		_ = saveCheckin115Cfg(cfg)
		return
	}
	if cfg.NextRun == 0 {
		cfg.NextRun = pickCheckinNextRun(now, cfg.TimeRange)
		_ = saveCheckin115Cfg(cfg)
		log.Printf("[115签到] ▶ 已安排下次执行：%s", time.Unix(cfg.NextRun, 0).Format("01-02 15:04"))
		return
	}
	if now.Unix() < cfg.NextRun {
		return
	}

	ok, _ := h.run115CheckinOnce(true)
	cfg = loadCheckin115Cfg()
	if ok {
		cfg.LastDone = today
		cfg.NextRun = pickCheckinNextRun(now.AddDate(0, 0, 1), cfg.TimeRange)
	} else {
		// 失败：当日剩余窗口内随机重试；窗口已过则明天
		cfg.NextRun = pickCheckinNextRun(now, cfg.TimeRange)
	}
	_ = saveCheckin115Cfg(cfg)
}

// ==================== HTTP 接口 ====================

// Checkin115GetConfig GET /115checkin/config → 配置 + 今日签到状态（实时查询）
func (h *Handler) Checkin115GetConfig(c *gin.Context) {
	cfg := loadCheckin115Cfg()
	signedToday := false
	statusErr := ""
	if cookie, err := h.get115Cookie(); err == nil && cookie != "" {
		if signed, serr := sign115Status(cookie); serr == nil {
			signedToday = signed
		} else if serr.Error() != "" {
			statusErr = serr.Error()
		}
	} else {
		statusErr = "未绑定 115 账号"
	}
	next := ""
	if cfg.NextRun > 0 {
		next = time.Unix(cfg.NextRun, 0).Format("01-02 15:04")
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"time_range":     cfg.TimeRange,
		"last_done":      cfg.LastDone,
		"next_run":       next,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"signed_today":   signedToday,
		"status_error":   statusErr,
	})
}

// Checkin115SaveConfig POST /115checkin/config {enabled, time_range}
func (h *Handler) Checkin115SaveConfig(c *gin.Context) {
	var req struct {
		Enabled   bool   `json:"enabled"`
		TimeRange string `json:"time_range"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if !reCheckinWindow.MatchString(strings.TrimSpace(req.TimeRange)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "时间窗口格式应为 HH:MM-HH:MM（如 06:00-09:00）"})
		return
	}
	cfg := loadCheckin115Cfg()
	cfg.Enabled = req.Enabled
	cfg.TimeRange = strings.TrimSpace(req.TimeRange)
	if cfg.Enabled && cfg.NextRun == 0 {
		cfg.NextRun = pickCheckinNextRun(time.Now(), cfg.TimeRange)
	}
	if err := saveCheckin115Cfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[配置] 115 签到：%v，窗口 %s", cfg.Enabled, cfg.TimeRange)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// Checkin115Run POST /115checkin/run → 立即签到
func (h *Handler) Checkin115Run(c *gin.Context) {
	ok, msg := h.run115CheckinOnce(false)
	if !ok {
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}
