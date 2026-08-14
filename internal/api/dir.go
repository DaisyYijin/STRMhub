package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"strmhub/internal/model"

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

	// 读取已保存的 115 Cookie
	var storage model.Storage
	if err := h.DB.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Cookie == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未绑定 115 账号，请先在「115账号」页扫码登录"})
		return
	}

	dirs, err := fetch115Dirs(storage.Cookie, cid)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": dirs, "cid": cid})
}

// fetch115Dirs 调用 115 webapi 获取目录下的文件夹列表
func fetch115Dirs(cookie, cid string) ([]gin.H, error) {
	query := url.Values{
		"aid":      {"1"},
		"cid":      {cid},
		"o":        {"user_ptime"},
		"asc":      {"0"},
		"offset":   {"0"},
		"show_dir": {"1"},
		"limit":    {"1150"},
		"natsort":  {"1"},
		"format":   {"json"},
		"fc_mix":   {"0"},
	}
	api := "https://webapi.115.com/files/?" + query.Encode()

	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua115)
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Cookie", cookie)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问 115 目录失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析 115 目录失败")
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

// ListLocalDirs 浏览本地文件系统目录
// GET /storage/local/dirs?path=C:\media
func (h *Handler) ListLocalDirs(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		// 空路径时返回盘符列表（Windows）
		c.JSON(http.StatusOK, gin.H{"data": listWindowsDrives(), "path": ""})
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
