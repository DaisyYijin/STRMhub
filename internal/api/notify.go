package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
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

// loadMessageConfig 从数据库加载消息通知配置
func loadMessageConfig() (*MessageConfig, error) {
	raw := settingValueCompat("message")
	if raw == "" {
		return nil, fmt.Errorf("尚未保存过消息配置：请到「消息配置」页填写并点击保存配置")
	}
	var cfg MessageConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("解析消息通知配置失败")
	}
	return &cfg, nil
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

// sendWecom 发送企业微信应用消息
func sendWecom(cfg WecomConfig, msg string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	// Step 1: 获取 access_token
	tokenURL := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		cfg.apiBase(), cfg.CorpID, cfg.Secret)

	resp, err := client.Get(tokenURL)
	if err != nil {
		log.Printf("企业微信获取 token 失败: %v", err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tokenResult struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResult); err != nil {
		return err
	}
	if tokenResult.ErrCode != 0 {
		log.Printf("企业微信获取 token 失败: %d %s", tokenResult.ErrCode, tokenResult.ErrMsg)
		return fmt.Errorf(tokenResult.ErrMsg)
	}

	// Step 2: 发送消息
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s",
		cfg.apiBase(), tokenResult.AccessToken)

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
		return fmt.Errorf(sendResult.ErrMsg)
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
		log.Printf("Telegram 发送消息失败: HTTP %d: %s", resp.StatusCode, string(body))
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
