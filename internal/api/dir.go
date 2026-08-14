package api

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"

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

	dirs, count, origin, err := ops.listDirs(cid)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": dirs, "cid": cid, "channel": ops.channelName(), "count": count, "origin": origin})
}

// fetch115Dirs 调用 115 webapi 获取目录下的文件夹列表（多镜像自动回退）
// 返回文件夹列表、目录总条目数、命中的域名
func fetch115Dirs(cookie, ua, cid string) ([]gin.H, int, string, error) {
	if cookie == "" {
		return nil, 0, "", fmt.Errorf("Cookie 为空，请先扫码登录")
	}
	log.Printf("[115目录] 请求 cid=%s, cookie长度=%d, UA=%s", cid, len(cookie), ua)

	entries, count, origin, err := fetch115FilesPage(cookie, ua, cid, 0)
	if err != nil {
		return nil, 0, "", err
	}

	// 只返回文件夹
	dirs := make([]gin.H, 0, len(entries))
	for _, d := range entries {
		if fmt.Sprint(d["f"]) == "0" {
			dirs = append(dirs, gin.H{"cid": fmt.Sprint(d["cid"]), "name": fmt.Sprint(d["n"])})
		}
	}
	return dirs, count, origin, nil
}

// build115FileQuery 构造 115 文件列表查询参数
// 与 p115client web 通道默认参数一致（asc/cid/cur/fc_mix/o/offset/limit/show_dir），
// 多余参数曾被怀疑参与触发风控，保持最简
func build115FileQuery(cid string, offset int) url.Values {
	return url.Values{
		"asc":      {"1"},
		"cid":      {cid},
		"cur":      {"1"},
		"fc_mix":   {"1"},
		"o":        {"user_ptime"},
		"offset":   {fmt.Sprint(offset)},
		"limit":    {"1150"},
		"show_dir": {"1"},
	}
}

// localDirLimit 本地目录单次返回上限（有界读取，大目录/网络挂载不全量扫描）
const localDirLimit = 1000

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

	// 有界读取：NAS 挂载（CIFS/NFS）下大目录全量 readdir + 排序可能要求数秒，
	// 只读前 localDirLimit+1 条即可判断截断；ReadDir(n) 不会预读整个目录
	f, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	entries, readErr := f.ReadDir(localDirLimit + 1)
	if readErr != nil && len(entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": readErr.Error()})
		return
	}
	truncated := len(entries) > localDirLimit
	if truncated {
		entries = entries[:localDirLimit]
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

	c.JSON(http.StatusOK, gin.H{"data": dirs, "path": path, "truncated": truncated})
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
