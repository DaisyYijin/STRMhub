package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 目录浏览 ====================

// List115Dirs 浏览 115 网盘目录（只返回文件夹）
// GET /storage/115/dirs?cid=0
func (h *Handler) List115Dirs(c *gin.Context) {
	cid := c.Query("cid")
	if cid == "" {
		cid = "0"
	}

	// 统一操作通道：OpenAPI 优先，Cookie 回退
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dirs, err := ops.listDirs(cid)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": dirs, "cid": cid, "channel": ops.channelName()})
}

// fetch115Dirs 调用 115 webapi 获取目录下的文件夹列表（UA 需匹配登录设备）
func fetch115Dirs(cookie, ua, cid string) ([]gin.H, error) {
	if cookie == "" {
		return nil, fmt.Errorf("Cookie 为空，请先扫码登录")
	}
	log.Printf("[115目录] 请求 cid=%s, cookie长度=%d, UA=%.60s...", cid, len(cookie), ua)

	query := build115FileQuery(cid, 0)
	body, err := httpGet115UA(fileListAPI, query, cookie, ua, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("访问 115 目录失败: %v", err)
	}

	// 记录原始响应用于调试（截断过长内容）
	bodyStr := string(body)
	if len(bodyStr) > 500 {
		bodyStr = bodyStr[:500] + "..."
	}
	log.Printf("[115目录] 响应: %s", bodyStr)

	// 解析响应，校验 state 字段
	var result struct {
		State  bool                      `json:"state"`
		Error  string                    `json:"error"`
		ErrNo  interface{}               `json:"errno"`
		Data   []map[string]interface{} `json:"data"`
		Count  int                       `json:"count"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析 115 目录失败: %v", err)
	}

	// 校验 115 返回状态
	if !result.State {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "Cookie 可能已过期"
		}
		return nil, fmt.Errorf("115 返回错误: %s", errMsg)
	}

	// 只返回文件夹
	dirs := make([]gin.H, 0, len(result.Data))
	for _, d := range result.Data {
		if fmt.Sprint(d["f"]) == "0" {
			dirs = append(dirs, gin.H{"cid": fmt.Sprint(d["cid"]), "name": fmt.Sprint(d["n"])})
		}
	}
	return dirs, nil
}

// build115FileQuery 构造 115 文件列表查询参数
// 参数与 AList/115driver 完全一致，参数差异可能触发 115 风控
func build115FileQuery(cid string, offset int) url.Values {
	return url.Values{
		"aid":              {"1"},
		"cid":              {cid},
		"o":                {"user_ptime"},
		"asc":              {"1"},
		"offset":           {fmt.Sprint(offset)},
		"show_dir":         {"1"},
		"limit":            {"1150"},
		"snap":             {"0"},
		"natsort":          {"0"},
		"record_open_time": {"1"},
		"format":           {"json"},
		"fc_mix":           {"0"},
	}
}

// ListLocalDirs 浏览本地文件系统目录
// GET /storage/local/dirs?path=C:\media
// 注意：浏览的是 StrmHub 进程所在机器（Docker 部署时是容器内文件系统，卷挂载点也在其中）
func (h *Handler) ListLocalDirs(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		// 空路径时返回顶层：Windows 枚举盘符，Linux/Docker 列根目录
		if runtime.GOOS == "windows" {
			c.JSON(http.StatusOK, gin.H{"data": listWindowsDrives(), "path": ""})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": listUnixRootDirs(), "path": ""})
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dirs := make([]gin.H, 0)
	// 父目录
	parent := filepath.Dir(path)
	if parent != path {
		dirs = append(dirs, gin.H{"path": parent, "name": ".."})
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, gin.H{"path": filepath.Join(path, e.Name()), "name": e.Name()})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return fmt.Sprint(dirs[i]["name"]) < fmt.Sprint(dirs[j]["name"]) })

	c.JSON(http.StatusOK, gin.H{"data": dirs, "path": path})
}

// listWindowsDrives 枚举 Windows 盘符
func listWindowsDrives() []gin.H {
	var drives []gin.H
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		p := string(letter) + ":\\"
		if _, err := os.Stat(p); err == nil {
			drives = append(drives, gin.H{"path": p, "name": p})
		}
	}
	return drives
}

// listUnixRootDirs 列出 Linux/Docker 根目录下的文件夹（/media、/data 等挂载点在此可见）
func listUnixRootDirs() []gin.H {
	entries, err := os.ReadDir("/")
	if err != nil {
		return []gin.H{}
	}
	dirs := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, gin.H{"path": "/" + e.Name(), "name": e.Name()})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return fmt.Sprint(dirs[i]["name"]) < fmt.Sprint(dirs[j]["name"]) })
	return dirs
}
