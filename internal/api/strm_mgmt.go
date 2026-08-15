package api

// ==================== STRM 管理增强 ====================
//
// 快扫：扫描 strm 表，对每条记录的 pickcode 做一次轻量 115 存在性校验
// 慢扫：完整获取下载直链验证可访问性
// 清理：删除失效 strm 的数据库记录 + 本地文件

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// FastScan 快扫：校验 strm 对应的 115 文件是否仍存在（不发下载请求）
// POST /strm/scan/fast
func (h *Handler) FastScan(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var files []model.StrmFile
	h.DB.Where("status = ?", "active").Find(&files)

	total, invalid := len(files), 0
	for i, f := range files {
		if i%20 == 0 && i > 0 {
			logStrmScan("快扫", i, total)
		}
		// 用 pickcode（即 fid）轻量查文件信息
		if f.StreamURL == "" {
			continue
		}
		// 从 strm URL 提取 pickcode：/d/{pickcode}/...
		pc := extractPickcodeFromURL(f.StreamURL)
		if pc == "" {
			continue
		}
		if _, _, err := get115DownloadURL(pc, cookie, ""); err != nil {
			// 获取直链失败 → 文件可能已删除
			h.DB.Model(&f).Update("status", "invalid")
			invalid++
		}
		// 全局节流
		throttle115("https://webapi.115.com/files/download")
	}

	msg := fmt.Sprintf("快扫完成: 共 %d 条，失效 %d 条", total, invalid)
	logStrmScanDone("快扫", total, invalid)
	c.JSON(http.StatusOK, gin.H{"message": msg, "total": total, "invalid": invalid})
}

// SlowScan 慢扫：完整验证下载直链可访问性
// POST /strm/scan/slow
func (h *Handler) SlowScan(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var files []model.StrmFile
	h.DB.Where("status = ?", "active").Find(&files)

	total, invalid := len(files), 0
	for i, f := range files {
		if i%10 == 0 && i > 0 {
			logStrmScan("慢扫", i, total)
		}
		pc := extractPickcodeFromURL(f.StreamURL)
		if pc == "" {
			continue
		}
		url, _, err := get115DownloadURL(pc, cookie, "")
		if err != nil {
			h.DB.Model(&f).Update("status", "invalid")
			invalid++
			continue
		}
		// HEAD 请求验证直链可达
		client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 不跟随重定向
		}}
		resp, err := client.Head(url)
		if err != nil || (resp != nil && resp.StatusCode >= 400) {
			h.DB.Model(&f).Update("status", "invalid")
			invalid++
		}
		if resp != nil {
			resp.Body.Close()
		}
		throttle115("https://webapi.115.com/files/download")
	}

	msg := fmt.Sprintf("慢扫完成: 共 %d 条，失效 %d 条", total, invalid)
	logStrmScanDone("慢扫", total, invalid)
	c.JSON(http.StatusOK, gin.H{"message": msg, "total": total, "invalid": invalid})
}

// CleanupInvalidEnhanced 清理失效 strm：删数据库记录 + 删本地文件
// POST /strm/cleanup
func (h *Handler) CleanupInvalidEnhanced(c *gin.Context) {
	var files []model.StrmFile
	h.DB.Where("status = ?", "invalid").Find(&files)

	dbDeleted, fileDeleted := 0, 0
	var fullCfg struct {
		LocalPath string `json:"local_path"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg)
	if fullCfg.LocalPath == "" {
		fullCfg.LocalPath = "/media"
	}

	for _, f := range files {
		// 删本地文件
		if f.LocalPath != "" {
			localFile := filepath.Join(fullCfg.LocalPath, f.LocalPath)
			if _, err := os.Stat(localFile); err == nil {
				if err := os.Remove(localFile); err == nil {
					fileDeleted++
				}
			}
		}
		// 删数据库记录
		if err := h.DB.Delete(&f).Error; err == nil {
			dbDeleted++
		}
	}

	msg := fmt.Sprintf("清理完成: 删除数据库记录 %d 条，本地文件 %d 个", dbDeleted, fileDeleted)
	c.JSON(http.StatusOK, gin.H{"message": msg, "db_deleted": dbDeleted, "file_deleted": fileDeleted})
}

// extractPickcodeFromURL 从 strm 文件的 URL 提取 pickcode（/d/{pickcode}/...）
func extractPickcodeFromURL(url string) string {
	// 格式: http://domain:port/d/{pickcode}/{filename}
	// 找 /d/ 之后的路径段
	for i := 0; i < len(url)-3; i++ {
		if url[i:i+3] == "/d/" {
			rest := url[i+3:]
			if j := indexByte(rest, '/'); j > 0 {
				return rest[:j]
			}
			return rest
		}
	}
	return ""
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func logStrmScan(mode string, done, total int) {
	log.Printf("[%s] 进度: %d/%d\n", mode, done, total)
}

func logStrmScanDone(mode string, total, invalid int) {
	log.Printf("[%s] 完成: 共 %d 条，失效 %d 条\n", mode, total, invalid)
}
