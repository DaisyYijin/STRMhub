package api

// ==================== 观影门户：Emby 播放引擎对接 ====================
//
// 门户 = 界面层（海报墙/分类/推荐）；播放引擎 = Emby：
//   - 拖进度秒跳（Emby 按需转码分片）
//   - 内嵌音轨/字幕切换（服务端处理，含烧录字幕）
//   - 编码不支持自动转码
// 门户通过反向代理访问 Emby（浏览器只需可达 6688），api_key 注入。
// 无 Emby 匹配时回退门户自带的 ffmpeg 转封装方案。

import (
	"encoding/json"
	"io"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// embyProxy 反向代理到 EMBY管理 配置的服务器（自动附 api_key）
func embyProxy(c *gin.Context) {
	base, apiKey, ok := portalEmbyInfo()
	if !ok {
		c.String(http.StatusServiceUnavailable, "未配置 Emby（系统配置 → EMBY管理）")
		return
	}
	target, _ := url.Parse(base)
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := c.Request.URL.Path
	c.Request.URL.Path = "/emby" + strings.TrimPrefix(orig, "/api/portal/emby")
	q := c.Request.URL.Query()
	q.Set("api_key", apiKey)
	c.Request.URL.RawQuery = q.Encode()
	proxy.ServeHTTP(c.Writer, c.Request)
}

// portalEmbyInfo 从设置读 Emby 连接
func portalEmbyInfo() (base, apiKey string, ok bool) {
	var h struct{ DB interface{ Model(interface{}) interface{} } }
	_ = h
	var cfg struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if json.Unmarshal([]byte(settingValueCompat("emby")), &cfg) != nil || cfg.ServerURL == "" {
		return "", "", false
	}
	return strings.TrimRight(cfg.ServerURL, "/"), cfg.APIKey, cfg.APIKey != ""
}

// embyGet 代理 GET 到 Emby（服务端间调用）
func embyGet(path string, params map[string]string) ([]byte, error) {
	base, apiKey, ok := portalEmbyInfo()
	if !ok {
		return nil, fmt.Errorf("未配置 Emby")
	}
	u := base + path
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	q.Set("api_key", apiKey)
	resp, err := http.Get(u + "?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Emby HTTP %d", resp.StatusCode)
	}
	return jsonReadAll(resp)
}

// portalEmbyPlay 解析台账条目对应的 Emby 条目与播放信息
// GET /api/portal/embyplay?key=<标题目录>&f=<文件名>
func portalEmbyPlay(c *gin.Context) {
	key := strings.Trim(c.Query("key"), "/")
	fname := c.Query("f")
	if key == "" || strings.Contains(key, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if _, _, ok := portalEmbyInfo(); !ok {
		c.JSON(http.StatusOK, gin.H{"found": false, "reason": "未配置 Emby"})
		return
	}
	segs := strings.Split(key, "/")
	title, year, _ := parseTitleDir(segs[len(segs)-1])
	isTV := len(segs) >= 2 && segs[1] == "剧集"

	// 1. 按标题搜 Movie/Series
	types := "Movie"
	if isTV {
		types = "Series"
	}
	body, err := embyGet("/Items", map[string]string{
		"SearchTerm": title, "IncludeItemTypes": types, "Limit": "8",
		"Recursive": "true",
		"Fields": "Path,ProductionYear",
	})
	log.Printf("[门户Emby] 搜索 %q(%s) 响应: %s", title, types, truncateStr(string(body), 300))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"found": false, "reason": "Emby 搜索失败: " + err.Error()})
		return
	}
	var res struct {
		Items []struct {
			Id    string `json:"Id"`
			Name  string `json:"Name"`
			Year  int    `json:"ProductionYear"`
			Path  string `json:"Path"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(body, &res)
	if len(res.Items) == 0 {
		// SearchTerm 未命中：用 NameStartsWith 再试（部分 Emby 版本对中文 SearchTerm 支持差）
		body2, err2 := embyGet("/Items", map[string]string{
			"NameStartsWith": title, "IncludeItemTypes": types, "Limit": "8",
			"Recursive": "true",
			"Fields": "Path,ProductionYear",
		})
		if err2 == nil {
			_ = json.Unmarshal(body2, &res)
		}
	}
	if len(res.Items) == 0 {
		c.JSON(http.StatusOK, gin.H{"found": false, "reason": "Emby 库中未找到：" + title + "（库可能还没扫描完）"})
		return
	}
	// 年份优先匹配
	item := res.Items[0]
	for _, it := range res.Items {
		if year != "" && fmt.Sprint(it.Year) == year {
			item = it
			break
		}
	}

	itemId := item.Id
	if isTV {
		// 2. 剧集：定位到具体集（按文件名匹配 Path）
		epBody, err := embyGet("/Shows/"+item.Id+"/Episodes", map[string]string{
			"UserId": "1", "Fields": "Path",
		})
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"found": false, "reason": "Emby 剧集查询失败"})
			return
		}
		var eps struct {
			Items []struct {
				Id   string `json:"Id"`
				Path string `json:"Path"`
			} `json:"Items"`
		}
		_ = json.Unmarshal(epBody, &eps)
		// ledger 文件名：xxx.mkv；Emby STRM 路径以 xxx.mkv.strm 结尾（同名匹配）
		want := strings.TrimSuffix(fname, ".strm")
		for _, ep := range eps.Items {
			if ep.Path != "" && (strings.Contains(ep.Path, want) ||
				strings.HasSuffix(strings.ToLower(ep.Path), strings.ToLower(want)+".strm")) {
				itemId = ep.Id
				break
			}
		}
		if itemId == item.Id && len(eps.Items) > 0 {
			itemId = eps.Items[0].Id // 未匹配到集时退第一集
		}
	}

	// 3. 取 MediaSources（音轨/字幕清单 + sourceId）
	srcBody, err := embyGet("/Items", map[string]string{
		"Ids": itemId, "Fields": "MediaSources",
	})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"found": false, "reason": "Emby 媒体源查询失败"})
		return
	}
	var itemDetail struct {
		Items []struct {
			MediaSources []struct {
				Id   string `json:"Id"`
				Name string `json:"Name"`
				MediaStreams []struct {
					Index       int    `json:"Index"`
					Type        string `json:"Type"`
					Codec       string `json:"Codec"`
					DisplayTitle string `json:"DisplayTitle"`
					Title       string `json:"Title"`
					Language    string `json:"Language"`
					IsTextSubtitleStream bool `json:"IsTextSubtitleStream"`
					IsExternal  bool     `json:"IsExternal"`
				} `json:"MediaStreams"`
			} `json:"MediaSources"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(srcBody, &itemDetail)
	if len(itemDetail.Items) == 0 || len(itemDetail.Items[0].MediaSources) == 0 {
		c.JSON(http.StatusOK, gin.H{"found": false, "reason": "Emby 无媒体源"})
		return
	}
	src := itemDetail.Items[0].MediaSources[0]
	audio := []gin.H{}
	subs := []gin.H{}
	for _, st := range src.MediaStreams {
		switch st.Type {
		case "Audio":
			label := st.DisplayTitle
			if label == "" {
				label = st.Language + " " + st.Codec
			}
			audio = append(audio, gin.H{"index": st.Index, "label": label})
		case "Subtitle":
			if !st.IsTextSubtitleStream && !st.IsExternal {
				continue // 图片字幕（PGS）HLS 模式也可烧录，但先只列文本
			}
			label := st.Title
			if label == "" {
				label = st.Language
			}
			if label == "" {
				label = fmt.Sprintf("字幕 %d", st.Index)
			}
			subs = append(subs, gin.H{"index": st.Index, "label": label, "external": st.IsExternal})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"found": true, "item_id": itemId, "source_id": src.Id,
		"audio": audio, "subs": subs,
		"url": "/api/portal/emby/videos/" + itemId + "/master.m3u8?MediaSourceId=" + url.QueryEscape(src.Id),
	})
}

func jsonReadAll(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
