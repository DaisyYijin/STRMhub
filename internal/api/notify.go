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

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 消息通知 ====================

// MessageConfig 消息通知配置
type MessageConfig struct {
	Wecom WecomConfig `json:"wecom"`
	TG    TGConfig    `json:"tg"`
}

// WecomConfig 企业微信配置
type WecomConfig struct {
	CorpID  string `json:"corp_id"`
	Secret  string `json:"secret"`
	AgentID string `json:"agent_id"`
	Enabled any    `json:"enabled"`
}

// TGConfig Telegram 配置
type TGConfig struct {
	Token   string `json:"token"`
	ChatID  string `json:"chat_id"`
	Enabled any    `json:"enabled"`
}

// loadMessageConfig 从数据库加载消息通知配置
func loadMessageConfig() (*MessageConfig, error) {
	var s model.Setting
	if err := model.DB.Where("key = ?", "message").First(&s).Error; err != nil {
		return nil, fmt.Errorf("未配置消息通知")
	}
	var cfg MessageConfig
	if err := json.Unmarshal([]byte(s.Value), &cfg); err != nil {
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
	tokenURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		cfg.CorpID, cfg.Secret)

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
	sendURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=%s",
		tokenResult.AccessToken)

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
	var s model.Setting
	if err := model.DB.Where("key = ?", "proxy").First(&s).Error; err != nil {
		return ""
	}
	var cfg struct {
		URL string `json:"url"`
	}
	json.Unmarshal([]byte(s.Value), &cfg)
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
