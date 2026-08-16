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
// 如果配置了路径替换规则，将本地路径转为 Emby 路径后调用 Emby Library scan API
func (h *Handler) notifyEmbyRefresh(localPath string) {
	var s model.Setting
	if err := h.DB.Where("key = ?", "emby-refresh").First(&s).Error; err != nil {
		return
	}
	var cfg struct {
		PathRule string `json:"path_rule"`
		Style    string `json:"style"`
		Enabled  any    `json:"enabled"`
	}
	if json.Unmarshal([]byte(s.Value), &cfg) != nil {
		return
	}
	// 检查是否启用
	enabled := true
	switch v := cfg.Enabled.(type) {
	case bool:
		enabled = v
	case string:
		enabled = v == "true"
	}
	if !enabled {
		return
	}

	// 路径替换
	embyPath := localPath
	if cfg.PathRule != "" && strings.Contains(cfg.PathRule, "#") {
		parts := strings.SplitN(cfg.PathRule, "#", 2)
		src, dst := parts[0], parts[1]
		if src != "" && strings.HasPrefix(localPath, src) {
			embyPath = dst + strings.TrimPrefix(localPath, src)
		}
	}
	if cfg.Style == "windows" {
		embyPath = strings.ReplaceAll(embyPath, "/", "\\")
	}

	// 读取 Emby webhook 配置中的 server 地址（暂用 webhook 地址的 host）
	var embySetting model.Setting
	if h.DB.Where("key = ?", "emby-notify").First(&embySetting).Error != nil {
		return
	}
	var embyCfg struct {
		Webhook string `json:"webhook"`
	}
	json.Unmarshal([]byte(embySetting.Value), &embyCfg)

	// 从 webhook 地址提取 Emby server
	if embyCfg.Webhook == "" {
		return
	}
	// webhook 格式: http://ip:port/api/emby/webhook?token=xxx
	u, err := url.Parse(embyCfg.Webhook)
	if err != nil {
		return
	}
	embyServer := u.Scheme + "://" + u.Host

	client := &http.Client{Timeout: 10 * time.Second}

	// 优先按库刷新（CMS 同款）：取媒体库列表，将变更路径映射到所属库后逐库 Refresh
	apiKey := ""
	var refreshCfg struct {
		ApiKey string `json:"api_key"`
	}
	if err := json.Unmarshal([]byte(s.Value), &refreshCfg); err != nil {
		return
	}
	apiKey = strings.TrimSpace(refreshCfg.ApiKey)
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
			}
			if refreshed > 0 {
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
