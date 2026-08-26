package api

import (
	"encoding/json"
	"fmt"
	"log"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== EMBY 入库刷新通知 ====================

// notifyEmbyRefresh STRM 生成后通知 Emby 刷新入库
// 统一使用 EMBY管理 配置（emby：服务器地址/API密钥/本地路径映射/路径风格/入库时刷新），
// 兼容旧版 emby-refresh（path_rule/enabled）与从 webhook 地址提取 host 的配置方式
func (h *Handler) notifyEmbyRefresh(localPath string) {
	var s model.Setting
	if err := h.DB.Where("key = ?", "emby").First(&s).Error; err != nil {
		return
	}
	var cfg struct {
		Style          string `json:"style"`
		RefreshEnabled any    `json:"refresh_enabled"`
	}
	if json.Unmarshal([]byte(s.Value), &cfg) != nil {
		return
	}
	// 检查是否启用；未写 refresh_enabled 时回退旧版 emby-refresh.enabled
	enabled := true
	switch v := cfg.RefreshEnabled.(type) {
	case bool:
		enabled = v
	case string:
		enabled = v == "true"
	default:
		var old struct {
			Enabled any `json:"enabled"`
		}
		if v2 := h.getSettingValue("emby-refresh"); v2 != "" && json.Unmarshal([]byte(v2), &old) == nil {
			if b, ok := old.Enabled.(bool); ok {
				enabled = b
			} else if str, ok := old.Enabled.(string); ok {
				enabled = str == "true"
			}
		}
	}
	if !enabled {
		return
	}

	// 路径替换：本地路径映射（emby.path_mapping，与建库插件/6086 反代共用）
	embyPath := h.mapToEmbyPath(localPath)
	style := cfg.Style
	if style == "" {
		var old struct {
			Style string `json:"style"`
		}
		if v2 := h.getSettingValue("emby-refresh"); v2 != "" && json.Unmarshal([]byte(v2), &old) == nil {
			style = old.Style
		}
	}
	if style == "windows" {
		embyPath = strings.ReplaceAll(embyPath, "/", "\\")
	}

	// Emby 地址与密钥：优先 EMBY管理 配置；未配置地址时回退从 webhook URL 提取（旧版）
	embyServer, apiKey, ok := h.embyServerInfo()
	if !ok {
		var embySetting model.Setting
		if h.DB.Where("key = ?", "emby-notify").First(&embySetting).Error != nil {
			return
		}
		var embyCfg struct {
			Webhook string `json:"webhook"`
		}
		json.Unmarshal([]byte(embySetting.Value), &embyCfg)
		// webhook 格式: http://ip:port/api/emby/webhook?token=xxx
		if embyCfg.Webhook == "" {
			return
		}
		u, err := url.Parse(embyCfg.Webhook)
		if err != nil {
			return
		}
		embyServer = u.Scheme + "://" + u.Host
		apiKey = ""
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// 优先按库刷新（CMS 同款）：取媒体库列表，将变更路径映射到所属库后逐库 Refresh
	apiKey = strings.TrimSpace(apiKey)
	q := ""
	if apiKey != "" {
		q = "?api_key=" + url.QueryEscape(apiKey)
	}
	libResp, err := client.Get(embyServer + "/Library/MediaFolders" + q)
	if err == nil {
		defer libResp.Body.Close()
		var libs struct {
			Items []struct {
				ID         string   `json:"Id"`
				Name       string   `json:"Name"`
				Locations  []string `json:"Locations"`
			} `json:"Items"`
		}
		if json.NewDecoder(libResp.Body).Decode(&libs) == nil {
			refreshed := 0
			libNames := ""
			for _, lib := range libs.Items {
				hit := false
				for _, loc := range lib.Locations {
					if strings.HasPrefix(embyPath, strings.TrimRight(loc, "/")+"/") || embyPath == loc {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
				r, err := client.Post(embyServer+"/Library/"+lib.ID+"/Refresh"+q, "", nil)
				if err != nil {
					log.Printf("Emby 按库刷新失败 %s: %v", lib.Name, err)
					continue
				}
				r.Body.Close()
				refreshed++
				log.Printf("Emby 媒体库刷新任务提交成功：%s %s", lib.ID, lib.Name)
				if libNames != "" {
					libNames += "、"
				}
				libNames += lib.Name
			}
			if refreshed > 0 {
				// 入库通知（Emby-notify 卡的消费方）：刷新已提交 = 内容即将入库
				go NotifyMessage("🎬 媒体入库", "已刷新媒体库：" + libNames)
				return
			}
			log.Printf("Emby 按库刷新：变更路径未命中任何媒体库（%s），回退路径通知", embyPath)
		}
	}

	// 回退：按路径通知
	// POST /Library/Media/Updated { "Updates": [{"Path":"...","UpdateType":"Created"}] }
	body, _ := json.Marshal(map[string]interface{}{
		"Updates": []map[string]string{
			{"Path": embyPath, "UpdateType": "Created"},
		},
	})
	req, _ := http.NewRequest("POST", embyServer+"/Library/Media/Updated"+q, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Emby 刷新通知失败: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("Emby 刷新通知已发送: %s", embyPath)
}

// TestEmbyConnection 测试 Emby 服务器连接
// POST /config/test-emby  body: {"server_url":"...", "api_key":"..."}
func (h *Handler) TestEmbyConnection(c *gin.Context) {
	var req struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ServerURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 Emby 服务器地址"})
		return
	}

	base := strings.TrimRight(req.ServerURL, "/")
	q := ""
	if req.APIKey != "" {
		q = "?api_key=" + url.QueryEscape(req.APIKey)
	}

	// 获取服务器信息
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(base + "/System/Info" + q)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "无法连接: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "API 密钥无效或未填写"})
		return
	}
	if resp.StatusCode != 200 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return
	}
	var info struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	_ = json.Unmarshal(body, &info)

	// 获取媒体库数量
	libraryCount := 0
	if resp2, err := client.Get(base + "/Library/MediaFolders" + q); err == nil {
		defer resp2.Body.Close()
		var libs struct {
			Items []struct {
				ID string `json:"Id"`
			} `json:"Items"`
		}
		body2, _ := io.ReadAll(resp2.Body)
		if json.Unmarshal(body2, &libs) == nil {
			libraryCount = len(libs.Items)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"server_name":   info.ServerName,
		"version":       info.Version,
		"library_count": libraryCount,
	})
}
