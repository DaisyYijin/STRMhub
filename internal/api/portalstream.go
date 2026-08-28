package api

// ==================== 观影门户：服务端转封装（ffmpeg remux → HLS） ====================
//
// 浏览器直出解不了 MKV/没有多音轨 API；这里用 ffmpeg 在服务端把 115 文件
// 无损转封装为 HLS（-c copy，不重编码，同 Emby「直接串流」模式），并提供：
//   /api/portal/probe?pick=  → ffprobe 识别内嵌音轨/字幕（codec/语言/标题）
//   /api/portal/hls?pick=&a=N → 启动转封装会话，返回 m3u8/分片
//   /api/portal/estr?pick=&s=N → 提取内嵌字幕轨为 WebVTT
// 会话空闲自动清理；ffmpeg 缺失时接口报错，前端回退直链播放。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// hlsSession 一个转封装会话（一次播放一个）
type hlsSession struct {
	dir       string
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	mu        sync.Mutex
	m3u8Ready chan struct{}
	lastHit   time.Time
	audioRel  int
}

var (
	hlsSessions   = map[string]*hlsSession{}
	hlsSessionsMu sync.Mutex
	probeCache    = map[string]portalProbeResult{}
	probeCacheMu  sync.Mutex
)

func ffmpegBin() string { return "/usr/bin/ffmpeg" }

func hasFFmpeg() bool {
	_, err := os.Stat(ffmpegBin())
	if err == nil {
		return true
	}
	// 开发机 / 自定义安装路径
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return true
	} else if p != "" {
		return true
	}
	b, _ := exec.Command("ffmpeg", "-version").CombinedOutput()
	return strings.Contains(string(b), "ffmpeg")
}

// portalProbeResult ffprobe 识别结果（缓存 10 分钟）
type portalProbeResult struct {
	Video  []probeStream `json:"video"`
	Audio  []probeStream `json:"audio"`
	Subs   []probeStream `json:"subs"`
	At     time.Time     `json:"-"`
}

type probeStream struct {
	Index    int    `json:"index"`     // ffmpeg 全局流索引
	Rel      int    `json:"rel"`       // 同类流内序号（音轨 0/1/2…）
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// portalProbe ffprobe 识别内嵌轨道
func portalProbe(c *gin.Context) {
	pick := c.Query("pick")
	if pick == "" || len(pick) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad pick"})
		return
	}
	probeCacheMu.Lock()
	if pr, ok := probeCache[pick]; ok && time.Since(pr.At) < 10*time.Minute {
		probeCacheMu.Unlock()
		c.JSON(http.StatusOK, pr)
		return
	}
	probeCacheMu.Unlock()

	if !hasFFmpeg() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务端未安装 ffmpeg，无法识别内嵌轨道"})
		return
	}
	urlStr, ua, err := portalPickURL(c, pick)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取直链失败: " + err.Error()})
		return
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-user_agent", ua,
		"-print_format", "json", "-show_streams", "-analyzeduration", "20M", urlStr}
	out, err := exec.Command("ffprobe", args...).CombinedOutput()
	if err != nil {
		// 兼容：ffprobe 可能也在 /usr/bin
		out2, err2 := exec.Command("/usr/bin/ffprobe", args...).CombinedOutput()
		if err2 != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "ffprobe 失败: " + truncateStr(string(out), 150)})
			return
		}
		out = out2
	}
	var pr portalProbeResult
	var wrap struct {
		Streams []struct {
			Index    int    `json:"index"`
			CodecName string `json:"codec_name"`
			CodecType string `json:"codec_type"`
			Channels int    `json:"channels,omitempty"`
			Disposition map[string]int `json:"disposition"`
			Tags     struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &wrap); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ffprobe 输出解析失败"})
		return
	}
	ai, si := 0, 0
	for _, st := range wrap.Streams {
		ps := probeStream{Index: st.Index, Codec: st.CodecName,
			Language: st.Tags.Language, Title: st.Tags.Title, Channels: st.Channels,
			Default: st.Disposition["default"] == 1}
		switch st.CodecType {
		case "video":
			pr.Video = append(pr.Video, ps)
		case "audio":
			ps.Rel = ai
			ai++
			pr.Audio = append(pr.Audio, ps)
		case "subtitle":
			ps.Rel = si
			si++
			pr.Subs = append(pr.Subs, ps)
		}
	}
	pr.At = time.Now()
	probeCacheMu.Lock()
	probeCache[pick] = pr
	probeCacheMu.Unlock()
	c.JSON(http.StatusOK, pr)
}

// portalPickURL pick_code → 115 直链（带签发 UA）
func portalPickURL(c *gin.Context, pick string) (urlStr, ua string, err error) {
	ua = c.Request.UserAgent()
	urlStr, err = proxyDownloadURL(model.DB, portalCfg, pick, ua)
	return
}

// portalHLS 启动/复用转封装会话；?pick=&a=<音轨序号>；返回 sid
func portalHLS(c *gin.Context) {
	// 兼容 JSON body 与 query 两种传参
	var body struct {
		Pick string `json:"pick"`
		A    int    `json:"a"`
	}
	_ = c.ShouldBindJSON(&body)
	pick := body.Pick
	if pick == "" {
		pick = c.Query("pick")
	}
	if pick == "" || len(pick) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad pick"})
		return
	}
	if !hasFFmpeg() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务端未安装 ffmpeg，无法转封装播放"})
		return
	}
	audioRel := body.A
	if audioRel == 0 {
		fmt.Sscanf(c.Query("a"), "%d", &audioRel)
	}

	urlStr, ua, err := portalPickURL(c, pick)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取直链失败: " + err.Error()})
		return
	}

	hlsSessionsMu.Lock()
	// 复用：同 pick + 同音轨且活跃
	for sid, ss := range hlsSessions {
		if ss.dir != "" && strings.Contains(ss.dir, pick) && time.Since(ss.lastHit) < 2*time.Minute {
			if ss.audioRel == audioRel {
				ss.lastHit = time.Now()
				hlsSessionsMu.Unlock()
				c.JSON(http.StatusOK, gin.H{"sid": sid, "m3u8": "/api/portal/hls?sid=" + sid})
				return
			}
		}
	}
	hlsSessionsMu.Unlock()

	dir, err := os.MkdirTemp("", "hlss-"+pick+"-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话目录失败"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	// ffmpeg：-c copy 无损转封装 HLS（MKV→TS）。字幕轨不进 HLS（单独提取）。
	segArgs := []string{
		"-hide_banner", "-loglevel", "error",
		"-user_agent", ua,
		"-i", urlStr,
		"-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d", audioRel),
		"-c", "copy",
		"-f", "hls",
		"-hls_time", "8",
		"-hls_list_size", "0",
		"-hls_flags", "append_list+omit_endlist",
		"-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"),
		filepath.Join(dir, "index.m3u8"),
	}
	cmd := exec.CommandContext(ctx, ffmpegBinOrPath(), segArgs...)
	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(dir)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "启动 ffmpeg 失败: " + err.Error()})
		return
	}
	sid := fmt.Sprintf("%d", time.Now().UnixNano())
	ss := &hlsSession{dir: dir, cmd: cmd, cancel: cancel, m3u8Ready: make(chan struct{}), lastHit: time.Now(), audioRel: audioRel}
	hlsSessionsMu.Lock()
	hlsSessions[sid] = ss
	hlsSessionsMu.Unlock()
	go func() {
		cmd.Wait()
		close(ss.m3u8Ready)
	}()
	c.JSON(http.StatusOK, gin.H{"sid": sid, "m3u8": "/api/portal/hls?sid=" + sid})
}

func ffmpegBinOrPath() string {
	if _, err := os.Stat(ffmpegBin()); err == nil {
		return ffmpegBin()
	}
	return "ffmpeg"
}

// portalHLSServe 提供会话的 m3u8 与分片
func portalHLSServe(c *gin.Context) {
	sid := c.Query("sid")
	f := strings.TrimPrefix(c.Param("file"), "/")
	if f == "" {
		f = "index.m3u8"
	}
	f = filepath.Base(f) // 防目录穿越
	hlsSessionsMu.Lock()
	ss, ok := hlsSessions[sid]
	if ok {
		ss.lastHit = time.Now()
	}
	hlsSessionsMu.Unlock()
	if !ok {
		c.String(http.StatusNotFound, "会话不存在或已过期")
		return
	}
	full := filepath.Join(ss.dir, f)
	if f == "index.m3u8" {
		// 等 m3u8 生成（最多 20 秒）
		select {
		case <-ss.m3u8Ready:
			c.String(http.StatusGone, "转封装已结束")
			return
		case <-time.After(20 * time.Second):
			c.String(http.StatusGatewayTimeout, "转封装超时")
			return
		default:
		}
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(full); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		c.Header("Cache-Control", "no-store")
		c.File(full)
		return
	}
	if _, err := os.Stat(full); err != nil {
		// 分片可能还没生成：等一小会（边转边播）
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(full); err == nil {
				break
			}
			select {
			case <-ss.m3u8Ready:
				c.String(http.StatusNotFound, "分片不存在")
				return
			default:
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	c.Header("Cache-Control", "no-store")
	c.File(full)
}

// portalExtractSub 提取内嵌字幕轨为 WebVTT（ffmpeg -c:s webvtt 直出）
func portalExtractSub(c *gin.Context) {
	pick := c.Query("pick")
	rel := 0
	fmt.Sscanf(c.Query("s"), "%d", &rel)
	if pick == "" || len(pick) > 64 {
		c.String(http.StatusBadRequest, "bad pick")
		return
	}
	if !hasFFmpeg() {
		c.String(http.StatusServiceUnavailable, "服务端未安装 ffmpeg")
		return
	}
	urlStr, ua, err := portalPickURL(c, pick)
	if err != nil {
		c.String(http.StatusBadGateway, "获取直链失败")
		return
	}
	cmd := exec.Command(ffmpegBinOrPath(),
		"-hide_banner", "-loglevel", "error",
		"-user_agent", ua,
		"-i", urlStr,
		"-map", fmt.Sprintf("0:s:%d", rel),
		"-c:s", "webvtt",
		"-f", "webvtt", "pipe:1")
	out, err := cmd.Output()
	if err != nil {
		c.String(http.StatusBadGateway, "字幕提取失败（该字幕轨编码可能是图片格式 PGS/VobSub，无法转文本）")
		return
	}
	c.Header("Access-Control-Allow-Origin", "*")
	c.Data(http.StatusOK, "text/vtt; charset=utf-8", out)
}

// hlsCleaner 空闲会话回收（2 分钟无访问即杀 ffmpeg 删分片）
func hlsCleaner() {
	for {
		time.Sleep(30 * time.Second)
		hlsSessionsMu.Lock()
		for sid, ss := range hlsSessions {
			if time.Since(ss.lastHit) > 2*time.Minute {
				ss.cancel()
				if ss.cmd.Process != nil {
					ss.cmd.Process.Kill()
				}
				os.RemoveAll(ss.dir)
				delete(hlsSessions, sid)
				log.Printf("[门户] 转封装会话回收：%s", sid)
			}
		}
		hlsSessionsMu.Unlock()
	}
}

var _ = sort.Strings
var _ = regexp.MustCompile
