package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// notifyConfigSource 配置读取源（SetupRoutes 注入）。
// 配置保存在 YAML（Config.SaveSetting），数据库 Setting 表仅旧版本使用；
// 包级函数无法拿 Handler，这里用注入的 Config 走"YAML 优先、DB 回退"读取。
var notifyConfigSource *config.Config

// settingValueCompat 读配置：YAML 优先，数据库回退（兼容旧数据）。
// 配置由前端 saveConfig → Config.SaveSetting 写入 YAML；只读 DB 的旧写法永远读不到。
func settingValueCompat(key string) string {
	if notifyConfigSource != nil {
		if v := notifyConfigSource.GetSetting(key); v != "" {
			return v
		}
	}
	if model.DB != nil {
		var s model.Setting
		if err := model.DB.Where("key = ?", key).First(&s).Error; err == nil {
			return s.Value
		}
	}
	return ""
}

// ==================== 消息通知 ====================

// MessageConfig 消息通知配置
type MessageConfig struct {
	Wecom WecomConfig `json:"wecom"`
	TG    TGConfig    `json:"tg"`
}

// WecomConfig 企业微信配置（Token/EncodingAESKey 为机器人回调验签解密用）
type WecomConfig struct {
	CorpID         string `json:"corp_id"`
	Secret         string `json:"secret"`
	AgentID        string `json:"agent_id"`
	Token          string `json:"token"`
	EncodingAESKey string `json:"encoding_aes_key"`
	APIURL         string `json:"api_url"`
	Enabled        any    `json:"enabled"`
}

// apiBase 企微 API 地址（海外部署可配反代；默认官方地址）
func (c *WecomConfig) apiBase() string {
	base := strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	return base
}

// TGConfig Telegram 配置
type TGConfig struct {
	Token   string `json:"token"`
	ChatID  string `json:"chat_id"`
	Enabled any    `json:"enabled"`
}

// msgCfgCache 消息配置短缓存：批量入库时每条通知都全量读盘解析 YAML
// 太浪费；5 秒 TTL，保存配置时主动失效（SaveSetting 钩子调用）
var msgCfgCache struct {
	sync.Mutex
	cfg *MessageConfig
	err error
	at  time.Time
}

// invalidateMsgCfgCache 消息配置保存后调用
func invalidateMsgCfgCache() {
	msgCfgCache.Lock()
	msgCfgCache.cfg, msgCfgCache.err, msgCfgCache.at = nil, nil, time.Time{}
	msgCfgCache.Unlock()
}

// loadMessageConfig 加载消息通知配置（YAML 优先/DB 回退，5 秒缓存）
func loadMessageConfig() (*MessageConfig, error) {
	msgCfgCache.Lock()
	cached, cacheAt := msgCfgCache.cfg, msgCfgCache.at
	msgCfgCache.Unlock()
	if cached != nil && time.Since(cacheAt) < 5*time.Second {
		return cached, nil
	}
	raw := settingValueCompat("message")
	var cfg *MessageConfig
	var err error
	if raw == "" {
		err = fmt.Errorf("尚未保存过消息配置：请到「消息配置」页填写并点击保存配置")
	} else {
		c := &MessageConfig{}
		if jerr := json.Unmarshal([]byte(raw), c); jerr != nil {
			err = fmt.Errorf("解析消息通知配置失败")
		} else {
			cfg = c
		}
	}
	if cfg != nil {
		msgCfgCache.Lock()
		msgCfgCache.cfg, msgCfgCache.at = cfg, time.Now()
		msgCfgCache.Unlock()
	}
	return cfg, err
}

// wecomTokenCache 企微 access_token 缓存（有效期约 7200 秒，提前 10 分钟刷新；
// 此前每条消息独立 gettoken，批量通知易撞企微 gettoken 频控）
var wecomTokenCache struct {
	sync.Mutex
	corpID string
	secret string
	token  string
	expire time.Time
}

func wecomTokenCached(cfg WecomConfig) (string, bool) {
	wecomTokenCache.Lock()
	defer wecomTokenCache.Unlock()
	if wecomTokenCache.token != "" && cfg.CorpID == wecomTokenCache.corpID &&
		cfg.Secret == wecomTokenCache.secret && time.Now().Before(wecomTokenCache.expire) {
		return wecomTokenCache.token, true
	}
	return "", false
}

func wecomTokenPut(cfg WecomConfig, token string, expiresIn int) {
	wecomTokenCache.Lock()
	defer wecomTokenCache.Unlock()
	wecomTokenCache.corpID, wecomTokenCache.secret = cfg.CorpID, cfg.Secret
	wecomTokenCache.token = token
	wecomTokenCache.expire = time.Now().Add(time.Duration(expiresIn)*time.Second - 10*time.Minute)
}

func (c *WecomConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

func (c *TGConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// NotifyMessage 推送消息到所有启用的通知渠道
func NotifyMessage(title, content string) {
	cfg, err := loadMessageConfig()
	if err != nil {
		log.Printf("[通知] ✗ 配置不可用，消息未发送（%s）: %v", title, err)
		return
	}

	fullMsg := title
	if content != "" {
		fullMsg = fmt.Sprintf("%s\n%s", title, content)
	}

	// 企业微信
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecom(cfg.Wecom, fullMsg)
	}

	// Telegram
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		go sendTelegram(cfg.TG, fullMsg)
	}
}

// NotifyMessageRich 富媒体通知：带封面图与链接。
// Telegram 直接发图片（sendPhoto）；企业微信应用消息发 news 图文卡片（picurl 外链封面）。
// posterURL 为空时自动退回纯文本通知。
func NotifyMessageRich(title, content, posterURL, linkURL string) {
	cfg, err := loadMessageConfig()
	if err != nil {
		log.Printf("[通知] ✗ 配置不可用，富通知未发送（%s）: %v", title, err)
		return
	}
	if posterURL == "" {
		NotifyMessage(title, content)
		return
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecomNews(cfg.Wecom, title, content, posterURL, linkURL)
	}
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		go sendTelegramPhoto(cfg.TG, title, content, posterURL)
	}
}

// wecomAccessToken 获取企业微信 access_token（带缓存）
func wecomAccessToken(cfg WecomConfig) (string, error) {
	if t, ok := wecomTokenCached(cfg); ok {
		return t, nil
	}
	client := &http.Client{Timeout: 10 * time.Second}
	tokenURL := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		cfg.apiBase(), cfg.CorpID, cfg.Secret)
	resp, err := client.Get(tokenURL)
	if err != nil {
		log.Printf("企业微信获取 token 失败: %v", err)
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tokenResult struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResult); err != nil {
		return "", err
	}
	if tokenResult.ErrCode != 0 {
		log.Printf("企业微信获取 token 失败: %d %s", tokenResult.ErrCode, tokenResult.ErrMsg)
		return "", fmt.Errorf("%s", tokenResult.ErrMsg)
	}
	if tokenResult.AccessToken != "" {
		wecomTokenPut(cfg, tokenResult.AccessToken, tokenResult.ExpiresIn)
	}
	return tokenResult.AccessToken, nil
}

// sendWecomNews 发送企业微信图文卡片（封面 + 标题 + 描述 + 链接）
func sendWecomNews(cfg WecomConfig, title, desc, picurl, linkURL string) error {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	if linkURL == "" {
		linkURL = "https://github.com/DaisyYijin/STRMhub"
	}
	payload := map[string]interface{}{
		"touser":  "@all",
		"msgtype": "news",
		"agentid": cfg.AgentID,
		"news": map[string]interface{}{
			"articles": []map[string]string{{
				"title":       truncateStr(title, 90),
				"description": truncateStr(desc, 300),
				"picurl":      picurl,
				"url":         linkURL,
			}},
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s", cfg.apiBase(), token)
	req, err := http.NewRequest("POST", sendURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("企业微信图文发送失败: %v", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(body, &r)
	if r.ErrCode != 0 {
		log.Printf("企业微信图文发送失败: %d %s（退回纯文本）", r.ErrCode, r.ErrMsg)
		// 图文被拒（如 picurl 不可达）时退回纯文本，保证消息不丢
		return sendWecom(cfg, title+"\n"+desc)
	}
	log.Printf("企业微信图文消息发送成功: %s", truncateStr(title, 40))
	return nil
}

// sendTelegramPhoto 发送 Telegram 图片（caption 带正文；失败退回文本）
func sendTelegramPhoto(cfg TGConfig, title, content, photoURL string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	if proxyURL := getProxyURL(); proxyURL != "" {
		if pu, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	caption := title
	if content != "" {
		caption += "\n" + content
	}
	form := url.Values{
		"chat_id": {cfg.ChatID},
		"photo":   {photoURL},
		"caption": {truncateStr(caption, 1000)},
	}
	resp, err := client.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", cfg.Token), form)
	if err != nil {
		log.Printf("Telegram 发送图片失败（退回文本）: %v", err)
		return sendTelegram(cfg, caption)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram 发送图片失败: HTTP %d %s（退回文本）", resp.StatusCode, truncateStr(string(body), 120))
		return sendTelegram(cfg, caption)
	}
	log.Printf("Telegram 图片消息发送成功: %s", truncateStr(title, 40))
	return nil
}

// sendWecom 发送企业微信应用消息
func sendWecom(cfg WecomConfig, msg string) error {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}

	// Step 2: 发送消息
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s",
		cfg.apiBase(), token)

	payload := map[string]interface{}{
		"touser":  "@all",
		"msgtype": "text",
		"agentid": cfg.AgentID,
		"text":    map[string]string{"content": msg},
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", sendURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp2, err := client.Do(req)
	if err != nil {
		log.Printf("企业微信发送消息失败: %v", err)
		return err
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)

	var sendResult struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	json.Unmarshal(body2, &sendResult)
	if sendResult.ErrCode != 0 {
		log.Printf("企业微信发送消息失败: %d %s", sendResult.ErrCode, sendResult.ErrMsg)
		return fmt.Errorf("%s", sendResult.ErrMsg)
	}

	log.Printf("企业微信消息发送成功: %s", msg)
	return nil
}

// sendTelegram 发送 Telegram 消息
func sendTelegram(cfg TGConfig, msg string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	sendURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.Token)

	payload := fmt.Sprintf(`{"chat_id":"%s","text":%s,"parse_mode":"HTML"}`,
		cfg.ChatID, mustJSON(msg))

	req, err := http.NewRequest("POST", sendURL, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// 通过代理发送（如果配置了代理）
	proxyURL := getProxyURL()
	if proxyURL != "" {
		if pu, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Telegram 发送消息失败: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram 发送消息失败: HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	log.Printf("Telegram 消息发送成功: %s", msg)
	return nil
}

// TestMessage 测试消息通知
// POST /message/test
func (h *Handler) TestMessage(c *gin.Context) {
	cfg, err := loadMessageConfig()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": err.Error()})
		return
	}

	testMsg := "StrmHub 消息通知测试"
	successCount := 0
	errorMsg := ""

	// 渠道状态预检：给出比"发送失败"更明确的指引
	if !cfg.Wecom.isEnabled() && !cfg.TG.isEnabled() {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "企业微信和 TG 渠道都是禁用状态：请启用对应渠道的「状态」开关后再保存并测试"})
		return
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "企业微信已启用但「企业 ID」为空：请填写后再保存并测试"})
		return
	}
	if cfg.TG.isEnabled() && cfg.TG.Token == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "TG 已启用但 Bot Token 为空：请填写后再保存并测试"})
		return
	}

	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" {
		if err := sendWecom(cfg.Wecom, testMsg); err != nil {
			errorMsg += "企业微信: " + err.Error() + "; "
		} else {
			successCount++
		}
	}

	if cfg.TG.isEnabled() && cfg.TG.Token != "" {
		if err := sendTelegram(cfg.TG, testMsg); err != nil {
			errorMsg += "Telegram: " + err.Error()
		} else {
			successCount++
		}
	}

	if successCount > 0 {
		c.JSON(http.StatusOK, gin.H{"success": true})
	} else {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "未启用任何通知渠道或发送失败: " + errorMsg})
	}
}

// getProxyURL 从数据库读取代理配置
func getProxyURL() string {
	var cfg struct {
		URL string `json:"url"`
	}
	json.Unmarshal([]byte(settingValueCompat("proxy")), &cfg)
	return cfg.URL
}

func parseProxyURL(proxyURL string) (func(*http.Request) (*url.URL, error), error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	return http.ProxyURL(u), nil
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
