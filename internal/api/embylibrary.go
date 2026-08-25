package api

// ==================== 插件：一键创建 Emby 媒体库 ====================
//
// 扫描本地媒体树的第二层目录（如 /media/俱乐部/电影/国产剧），
// 每个目录建成一个同名 Emby 媒体库（库名=国产剧）。
// 默认偏好：中文元数据 + NFO/图片保存到媒体文件夹 + 中文内容偏好。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// embyLibCandidate 待建库候选
type embyLibCandidate struct {
	Name      string `json:"name"`       // 库名（目录名）
	Dir       string `json:"dir"`        // 本地目录
	EmbyPath  string `json:"emby_path"`  // 映射后的 Emby 路径
	Type      string `json:"type"`       // movies / tvshows / mixed
	TypeLabel string `json:"type_label"` // 电影 / 剧集 / 混合
	Exists    bool   `json:"exists"`     // Emby 已有同名库
}

// guessLibType 按目录名推断库类型（沿父层目录名：/电影/国产剧 → movies）
func guessLibType(parentName, dirName string) (string, string) {
	p, d := strings.ToLower(parentName), strings.ToLower(dirName)
	combined := p + "/" + d
	switch {
	case strings.Contains(combined, "电影") || strings.Contains(combined, "movie") || strings.Contains(combined, "film"):
		if strings.Contains(combined, "剧") && !strings.Contains(combined, "电影") {
			return "tvshows", "剧集"
		}
		return "movies", "电影"
	case strings.Contains(combined, "剧集") || strings.Contains(combined, "电视") || strings.Contains(combined, "tv") || strings.Contains(combined, "剧"):
		return "tvshows", "剧集"
	case strings.Contains(combined, "音乐") || strings.Contains(combined, "music"):
		return "music", "音乐"
	case strings.Contains(combined, "动漫") || strings.Contains(combined, "anime") || strings.Contains(combined, "番剧"):
		return "tvshows", "剧集"
	default:
		return "mixed", "混合"
	}
}

// mapToEmbyPath 本地路径 → Emby 路径（复用 emby 配置的映射规则）
func (h *Handler) mapToEmbyPath(local string) string {
	var embyCfg struct {
		PathMapping string `json:"path_mapping"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("emby")), &embyCfg)
	if embyCfg.PathMapping != "" && strings.Contains(embyCfg.PathMapping, "#") {
		parts := strings.SplitN(embyCfg.PathMapping, "#", 2)
		src, dst := strings.TrimRight(parts[0], "/"), strings.TrimRight(parts[1], "/")
		if src != "" && strings.HasPrefix(local, src+"/") || local == src {
			return dst + strings.TrimPrefix(local, src)
		}
	}
	return local
}

// scanLibCandidates 扫描媒体库根下第二层目录（根/分类/子类 形态；
// 只有第一层时退化为第一层）
func (h *Handler) scanLibCandidates() ([]embyLibCandidate, error) {
	local := defaultLocalPath
	var fullCfg struct {
		LocalPath string `json:"local_path"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg) == nil && fullCfg.LocalPath != "" {
		local = fullCfg.LocalPath
	}

	var candidates []embyLibCandidate
	// 第一层（电影/剧集/AV…）
	l1, err := os.ReadDir(local)
	if err != nil {
		return nil, fmt.Errorf("读取媒体根失败: %v", err)
	}
	for _, e1 := range l1 {
		if !e1.IsDir() {
			continue
		}
		l1Path := filepath.Join(local, e1.Name())
		// 第二层（国产剧/日韩剧…）
		l2, err := os.ReadDir(l1Path)
		if err != nil {
			continue
		}
		hasSub := false
		for _, e2 := range l2 {
			if !e2.IsDir() {
				continue
			}
			hasSub = true
			l2Path := filepath.Join(l1Path, e2.Name())
			typ, label := guessLibType(e1.Name(), e2.Name())
			candidates = append(candidates, embyLibCandidate{
				Name:     e2.Name(),
				Dir:      l2Path,
				EmbyPath: h.mapToEmbyPath(l2Path),
				Type:     typ, TypeLabel: label,
			})
		}
		// 第一层无子目录（扁平结构）→ 第一层自身作为库
		if !hasSub {
			typ, label := guessLibType(e1.Name(), "")
			candidates = append(candidates, embyLibCandidate{
				Name:     e1.Name(),
				Dir:      l1Path,
				EmbyPath: h.mapToEmbyPath(l1Path),
				Type:     typ, TypeLabel: label,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return candidates, nil
}

// embyServerInfo 取 Emby 地址与 API Key（复用 EMBY 管理卡配置）
func (h *Handler) embyServerInfo() (base, apiKey string, ok bool) {
	var cfg struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("emby")), &cfg) != nil || cfg.ServerURL == "" {
		return "", "", false
	}
	base = strings.TrimRight(cfg.ServerURL, "/")
	apiKey = cfg.APIKey
	return base, apiKey, true
}

// EmbyLibrariesPreview GET /plugin/emby-libraries —— 预览待建库清单
func (h *Handler) EmbyLibrariesPreview(c *gin.Context) {
	cands, err := h.scanLibCandidates()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 已有库标记
	base, apiKey, ok := h.embyServerInfo()
	existing := map[string]bool{}
	if ok {
		q := ""
		if apiKey != "" {
			q = "?api_key=" + url.QueryEscape(apiKey)
		}
		if resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/Library/MediaFolders" + q); err == nil {
			defer resp.Body.Close()
			var libs struct {
				Items []struct {
					Name string `json:"Name"`
				} `json:"Items"`
			}
			if json.NewDecoder(resp.Body).Decode(&libs) == nil {
				for _, it := range libs.Items {
					existing[it.Name] = true
				}
			}
		}
	}
	for i := range cands {
		cands[i].Exists = existing[cands[i].Name]
	}
	c.JSON(http.StatusOK, gin.H{"data": cands, "emby_configured": ok})
}

// EmbyLibrariesCreate POST /plugin/emby-libraries —— 创建预览中不存在的库
func (h *Handler) EmbyLibrariesCreate(c *gin.Context) {
	base, apiKey, ok := h.embyServerInfo()
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 Emby 服务器（EMBY 管理卡）"})
		return
	}
	var req struct {
		Items []embyLibCandidate `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	q := ""
	if apiKey != "" {
		q = "?api_key=" + url.QueryEscape(apiKey)
	}
	client := &http.Client{Timeout: 20 * time.Second}

	created, skipped := []string{}, []string{}
	for _, it := range req.Items {
		if it.EmbyPath == "" {
			continue
		}
		// 默认库选项：中文元数据 + NFO/图片保存到媒体文件夹
		options := map[string]interface{}{
			"MetadataOptions": map[string]interface{}{
				"PreferredMetadataLanguage": "zh-CN",
				"MetadataCountryCode":       "cn",
				"MaxBackdrops":              10,
				"MinBackdropWidth":          1920,
			},
			"EnableInternetProviders":    true,
			"SaveLocalMetadata":          true, // 元数据（NFO/图片）保存到媒体文件夹
			"EnableAutomaticSeriesGrouping": false,
			"PreferredImageLanguage":     "zh-CN",
			"DownloadImagesInAdvance":    false,
			"PathInfos": []map[string]interface{}{
				{"Path": it.EmbyPath},
			},
		}
		body, _ := json.Marshal(options)
		api := base + "/Library/VirtualFolders" + q +
			"&name=" + url.QueryEscape(it.Name) +
			"&collectionType=" + it.Type +
			"&refreshLibrary=false"
		req2, _ := http.NewRequest(http.MethodPost, api, strings.NewReader(string(body)))
		req2.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req2)
		if err != nil {
			skipped = append(skipped, it.Name+"（请求失败）")
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			skipped = append(skipped, fmt.Sprintf("%s（HTTP %d）", it.Name, resp.StatusCode))
			continue
		}
		created = append(created, it.Name)
	}
	msg := fmt.Sprintf("创建 %d 个媒体库", len(created))
	if len(skipped) > 0 {
		msg += fmt.Sprintf("，失败 %d 个: %s", len(skipped), strings.Join(skipped, "、"))
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "created": created, "skipped": skipped})
}
