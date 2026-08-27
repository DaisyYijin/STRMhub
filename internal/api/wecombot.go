package api

// ==================== 企业微信机器人（双向互动） ====================
//
// 回调地址：http://<公网IP>:6086/wecom/callback
//   GET  = 企业微信后台的 URL 验证（解密 echostr 回显）
//   POST = 用户发给应用的消息（AES 加密 XML）→ 指令路由 → 异步回复
// 指令：下载 <链接> / 状态 / 搜索 <片名> / 整理 / 同步 / 帮助

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// wecomCallbackCfg 从消息配置读取的回调三要素
type wecomCallbackCfg struct {
	Token   string
	AESKey  string
	CorpID  string
	Enabled bool
}

func loadWecomCallback() (*wecomCallbackCfg, error) {
	cfg, err := loadMessageConfig()
	if err != nil {
		return nil, err
	}
	cb := &wecomCallbackCfg{
		Token:   cfg.Wecom.Token,
		AESKey:  cfg.Wecom.EncodingAESKey,
		CorpID:  cfg.Wecom.CorpID,
		Enabled: cfg.Wecom.isEnabled(),
	}
	if cb.Token == "" || cb.AESKey == "" {
		return nil, fmt.Errorf("未配置 Token / EncodingAESKey（消息配置卡）")
	}
	return cb, nil
}

// WecomCallback GET=URL验证 POST=消息接收
func (h *Handler) WecomCallback(c *gin.Context) {
	cb, err := loadWecomCallback()
	if err != nil {
		c.String(http.StatusServiceUnavailable, "callback not configured")
		return
	}
	key, err := newWecomAESKey(cb.AESKey)
	if err != nil {
		c.String(http.StatusInternalServerError, "aes key invalid")
		return
	}

	if c.Request.Method == http.MethodGet {
		// URL 验证：验签后解密 echostr，回显明文
		msgSig := c.Query("msg_signature")
		timestamp, nonce, echostr := c.Query("timestamp"), c.Query("nonce"), c.Query("echostr")
		if wecomSign(cb.Token, timestamp, nonce, echostr) != msgSig {
			c.String(http.StatusForbidden, "sign mismatch")
			return
		}
		plain, derr := key.wecomDecrypt(echostr)
		if derr != nil {
			c.String(http.StatusBadRequest, "decrypt fail: %v", derr)
			return
		}
		c.String(http.StatusOK, string(plain))
		return
	}

	// POST：收消息
	body, _ := io.ReadAll(c.Request.Body)
	var env struct {
		XMLName    xml.Name `xml:"xml"`
		ToUserName string   `xml:"ToUserName"`
		Encrypt    string   `xml:"Encrypt"`
	}
	if xml.Unmarshal(body, &env) != nil || env.Encrypt == "" {
		c.String(http.StatusOK, "success") // 格式异常也回 success，防企业微信重试轰炸
		return
	}
	msgSig := c.Query("msg_signature")
	timestamp, nonce := c.Query("timestamp"), c.Query("nonce")
	if wecomSign(cb.Token, timestamp, nonce, env.Encrypt) != msgSig {
		c.String(http.StatusForbidden, "sign mismatch")
		return
	}
	plainXML, derr := key.wecomDecrypt(env.Encrypt)
	if derr != nil {
		c.String(http.StatusOK, "success")
		return
	}
	var msg struct {
		XMLName    xml.Name `xml:"xml"`
		ToUserName string   `xml:"ToUserName"` // 企业 corpid
		FromUser   string   `xml:"FromUserName"`
		MsgType    string   `xml:"MsgType"`
		Content    string   `xml:"Content"`
		CreateTime int64    `xml:"CreateTime"`
	}
	if xml.Unmarshal(plainXML, &msg) != nil || msg.MsgType != "text" {
		c.String(http.StatusOK, "success")
		return
	}
	wecomCorpIDCache = msg.ToUserName

	// 5 秒内必须应答：指令异步执行 + 异步回复
	go h.wecomHandleCommand(strings.TrimSpace(msg.Content))
	c.String(http.StatusOK, "success")
}

// wecomHandleCommand 指令路由（异步执行，结果用应用消息推回）
func (h *Handler) wecomHandleCommand(text string) {
	if text == "" {
		return
	}
	reply := func(lines ...string) {
		NotifyMessage("🤖 StrmHub", strings.Join(lines, "\n"))
	}
	lower := strings.ToLower(text)
	// 直接发链接（无需"下载"前缀）：磁力/ed2k/http/115分享 一键触发
	if classifyLink(strings.TrimSpace(text)) != "" {
		h.wecomHandleLink(strings.TrimSpace(text), reply)
		return
	}
	switch {
	case lower == "帮助" || lower == "help" || lower == "?":
		reply(
			"可用指令：",
			"直接发链接 — 磁力/ed2k/HTTP 提交离线下载；115 分享链接自动转存（自动整理入库）",
			"下载 <链接> — 同上",
			"状态 — 任务状态 + 转存目录 + 离线任务",
			"搜索 <片名> — TMDB 搜片",
			"整理 — 立即执行一次整理",
			"同步 — 立即执行一次增量同步",
			"补全 — 扫描缺画质信息的文件，探测后规范命名",
		)

	case strings.HasPrefix(text, "下载"), strings.HasPrefix(lower, "dl "):
		link := strings.TrimSpace(strings.TrimPrefix(text, "下载"))
		if strings.HasPrefix(lower, "dl ") {
			link = strings.TrimSpace(text[3:])
		}
		if classifyLink(link) == "" {
			reply("✗ 无法识别链接类型，支持：磁力 / ed2k / HTTP / 115分享")
			return
		}
		h.wecomHandleLink(link, reply)

	case lower == "状态" || lower == "status":
		running, name, since := TaskStatus()
		lines := []string{}
		if running {
			lines = append(lines, fmt.Sprintf("▶ 任务运行中：%s（已 %s）", name, time.Since(since).Truncate(time.Second)))
		} else {
			lines = append(lines, "○ 当前无任务运行")
		}
		if cid := h.shareFolderCid(); cid != "" {
			// 115 目录查询加超时保护：网盘响应慢时不能拖住整个状态回复
			type listRes struct{ count int; ok bool }
			ch := make(chan listRes, 1)
			go func() {
				if ops, err := h.newPan115Ops(); err == nil {
					if entries, _, lerr := ops.listEntries(cid, 0); lerr == nil {
						ch <- listRes{len(entries), true}
						return
					}
				}
				ch <- listRes{}
			}()
			select {
			case r := <-ch:
				if r.ok {
					lines = append(lines, fmt.Sprintf("转存目录待处理：%d 个条目", r.count))
				}
			case <-time.After(5 * time.Second):
				lines = append(lines, "转存目录待处理：查询超时（115 响应慢）")
			}
		}
		if runs := GetRecentRuns(); len(runs) > 0 {
			lines = append(lines, "最近任务：")
			for _, r := range runs {
				mark := "✓"
				if !r.OK {
					mark = "✗"
				}
				lines = append(lines, fmt.Sprintf("  %s %s %s（%s）", mark, r.Name, r.Start, r.Elapsed))
			}
		}
		reply(lines...)

	case strings.HasPrefix(text, "搜索"), strings.HasPrefix(lower, "so "):
		q := strings.TrimSpace(strings.TrimPrefix(text, "搜索"))
		if strings.HasPrefix(lower, "so ") {
			q = strings.TrimSpace(text[3:])
		}
		if q == "" {
			reply("用法：搜索 <片名>")
			return
		}
		results := h.wecomSearchTMDB(q)
		if len(results) == 0 {
			reply("未找到: "+q)
			return
		}
		lines := []string{fmt.Sprintf("TMDB 搜索 %q：", q)}
		for _, r := range results {
			lines = append(lines, r)
		}
		reply(lines...)

	case lower == "补全" || lower == "enrich":
		if !loadEnrichPolicy().Enabled {
			reply("补全功能未开启（自动整理 → 基础配置 → 媒体补全）")
			return
		}
		go func() {
			if _, _, err := h.executeEnrichScan(); err != nil {
				NotifyMessage("🤖 StrmHub", "✗ 补全扫描失败: "+err.Error())
			} else {
				NotifyMessage("🤖 StrmHub", "✓ 补全扫描完成，任务已入队（详见日志）")
			}
		}()
		reply("已开始扫描媒体库，缺画质信息的文件将入队探测。")

	case lower == "整理":
		go func() {
			if _, _, err := h.executeOrganize(false); err != nil {
				NotifyMessage("🤖 StrmHub", "✗ 整理失败: "+err.Error())
			} else {
				NotifyMessage("🤖 StrmHub", "✓ 整理完成（详见日志）")
			}
		}()
		reply("已开始整理，完成后通知。")

	case lower == "同步":
		go func() {
			p := h.incrParamsFromConfig()
			if _, err := h.executeIncrementalSync(p); err != nil {
				NotifyMessage("🤖 StrmHub", "✗ 增量同步失败: "+err.Error())
			} else {
				NotifyMessage("🤖 StrmHub", "✓ 增量同步完成（详见日志）")
			}
		}()
		reply("已开始增量同步，完成后通知。")

	default:
		reply("未识别的指令。发送「帮助」查看可用指令。")
	}
}

// wecomHandleLink 企微消息里的下载链接分流：
// 115 分享链接 → shareReceiveCore 转存（自动整理）；磁力/ed2k/http → 115 离线下载
func (h *Handler) wecomHandleLink(link string, reply func(lines ...string)) {
	if classifyLink(link) == "share" {
		shareURL, code := splitShareLink(link)
		if code == "" {
			reply("✗ 115 分享链接缺少提取码：",
				"把链接和提取码发在一起即可，例如：",
				"https://115.com/s/abc123?password=xxxx",
				"或：https://115.com/s/abc123 提取码：xxxx")
			return
		}
		organize := true // 机器人触发的转存默认走「整理+增量」闭环
		reply("⏳ 开始转存 115 分享…", truncateStr(shareURL, 70))
		go func() {
			msg, ok, fail, err := h.shareReceiveCore(shareURL, code, "", organize)
			if err != nil {
				NotifyMessage("🤖 StrmHub", "✗ 转存失败: "+err.Error())
				return
			}
			if ok == 0 && fail == 0 {
				NotifyMessage("🤖 StrmHub", "分享为空，未转存任何内容")
				return
			}
			NotifyMessage("🤖 StrmHub", "✓ "+msg+"（整理入库已自动触发）")
		}()
		return
	}
	if err := h.submitOfflineLink(link); err != nil {
		reply("✗ 提交失败: " + err.Error())
		return
	}
	reply("✓ 已提交离线下载：", truncateStr(link, 80), "下载完成后自动整理入库并通知。")
}

// splitShareLink 从消息中拆出分享链接与提取码。
// 支持：URL 自带 ?password=xxx；"链接 提取码：xxx"；"链接 xxx"（末尾独立字段）
func splitShareLink(msg string) (shareURL, code string) {
	fields := strings.Fields(msg)
	if len(fields) == 0 {
		return "", ""
	}
	shareURL = fields[0]
	if u, err := url.Parse(shareURL); err == nil {
		if p := u.Query().Get("password"); p != "" {
			return shareURL, p
		}
	}
	// 其余字段里找提取码（"提取码：xxx" / "密码:xxx" / 纯 4-8 位字母数字）
	for i := 1; i < len(fields); i++ {
		f := strings.TrimPrefix(strings.TrimPrefix(fields[i], "提取码"), "密码")
		f = strings.Trim(f, "：:，, ")
		if len(f) >= 3 && len(f) <= 12 && isShareCode(f) {
			return shareURL, f
		}
	}
	return shareURL, ""
}

func isShareCode(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return len(s) > 0
}

// wecomSearchTMDB 搜片（独立轻实现：不加载完整整理客户端）
func (h *Handler) wecomSearchTMDB(q string) []string {
	tc, err := loadTmdbClient(nil)
	if err != nil {
		return []string{"TMDB 未配置: " + err.Error()}
	}
	parsed := &ParsedName{Title: q}
	media, err := tc.recognize(parsed)
	if err != nil || media == nil {
		return nil
	}
	typ := "电影"
	if media.MediaType == "tv" {
		typ = "剧集"
	}
	return []string{fmt.Sprintf("%s《%s》(%s) tmdb=%d — 可发送：下载 <磁力链接> 提交", typ, media.Title, media.Year, media.TmdbID)}
}

// submitOfflineLink 提交离线下载（磁力/ed2k/HTTP 走 web lixian 接口）
func (h *Handler) submitOfflineLink(rawURL string) error {
	cookie, err := h.get115Cookie()
	if err != nil {
		return err
	}
	form := url.Values{"url": {rawURL}}
	if target := h.shareFolderCid(); target != "" {
		form.Set("wp_path_id", target)
	}
	body, err := post115Form("https://115.com/web/lixian/?ac=add_task_url", form, cookie, ua115Unified(), 20*time.Second)
	if err != nil {
		return err
	}
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.State {
		return fmt.Errorf("115 拒绝: %s", truncateStr(string(body), 100))
	}
	log.Printf("[机器人] ✓ 离线下载已提交: %s", truncateStr(rawURL, 60))
	return nil
}
