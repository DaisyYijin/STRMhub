package api

// ==================== 影视刮削（原生 NFO + 海报到本地媒体库） ====================
//
// 直接生成 Emby/Kodi 标准元数据，替代"Emby 刮削到本地"这半段：
//   按 MediaLibrary(TmdbID) 拉 TMDB 详情 → 写 movie.nfo / tvshow.nfo
//   + poster.jpg / fanart.jpg / seasonNN-poster.jpg 到本地媒体库对应片目目录，
//   落盘后由「监控上传」自动回传 115 对应目录。
// Emby 侧建议把元数据读取器设为仅 NFO（以本站数据为准），避免二次刮削覆盖。
//
// 接口：GET/POST /scrape/config、POST /scrape/run、GET /scrape/status

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"strmhub/internal/model"
)

type scrapeCfg struct {
	LocalRoot   string `json:"local_root"`
	WriteNFO    bool   `json:"write_nfo"`
	WriteImages bool   `json:"write_images"`
	Force       bool   `json:"force"` // 覆盖已存在的元数据文件
}

func loadScrapeCfg() scrapeCfg {
	c := scrapeCfg{WriteNFO: true, WriteImages: true}
	if v := settingValueCompat("scrape"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	c.LocalRoot = strings.TrimRight(strings.TrimSpace(c.LocalRoot), "/")
	return c
}

func saveScrapeCfg(c scrapeCfg) error {
	b, _ := json.Marshal(c)
	return notifyConfigSource.SaveSetting("scrape", string(b))
}

// ---- 运行状态 ----

type scrapeStatus struct {
	Running bool     `json:"running"`
	Total   int      `json:"total"`
	Done    int      `json:"done"`
	Failed  int      `json:"failed"`
	Current string   `json:"current"`
	Errors  []string `json:"errors"`
}

var (
	scrapeMu       sync.Mutex
	scrapeSt       scrapeStatus
	scrapeStopFlag bool
)

func scrapeStatusSnapshot() scrapeStatus {
	scrapeMu.Lock()
	defer scrapeMu.Unlock()
	st := scrapeSt
	st.Errors = append([]string(nil), scrapeSt.Errors...)
	return st
}

func scrapeAddErr(format string, args ...any) {
	scrapeMu.Lock()
	defer scrapeMu.Unlock()
	scrapeSt.Failed++
	if len(scrapeSt.Errors) < 20 {
		scrapeSt.Errors = append(scrapeSt.Errors, fmt.Sprintf(format, args...))
	}
}

// Mukaku 风格的图片拉取：走 TMDB 配置的图床/代理（国内直连常不通）
func tmdbFetchImageBytes(imgPath string) ([]byte, error) {
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil || cfg.ImageApiUrl == "" {
		return nil, fmt.Errorf("TMDB 图床未配置")
	}
	base := strings.TrimRight(cfg.ImageApiUrl, "/")
	if !strings.HasSuffix(base, "/t/p") {
		base += "/t/p"
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/original"+imgPath, nil)
	client := &http.Client{Timeout: 20 * time.Second}
	proxyURL := getProxyURL()
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxyURL = cfg.ProxyUrl
	}
	if proxyURL != "" {
		if pu, perr := parseProxyURL(proxyURL); perr == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}

// ---- NFO 结构（Kodi/Emby 标准） ----

type nfoRating struct {
	XMLName xml.Name `xml:"rating"`
	Name    string   `xml:"name,attr"`
	Max     int      `xml:"max,attr"`
	Default bool     `xml:"default,attr"`
	Value   float64  `xml:"value"`
}

type nfoActor struct {
	Name string `xml:"name"`
	Role string `xml:"role"`
}

// nfoUniqueID Kodi 多唯一 ID 元素（同名不同 type 属性）；encoding/xml
// 不允许两个同名字段重复，需用切片
type nfoUniqueID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

type nfoMovie struct {
	XMLName       xml.Name      `xml:"movie"`
	Title         string        `xml:"title"`
	OriginalTitle string        `xml:"originaltitle"`
	Ratings       []nfoRating   `xml:"ratings>rating"`
	Year          string        `xml:"year"`
	Premiered     string        `xml:"premiered"`
	Runtime       int           `xml:"runtime"`
	UniqueIDs     []nfoUniqueID `xml:"uniqueid"`
	Genres        []string      `xml:"genre"`
	Directors     []string      `xml:"director"`
	Studios       []string      `xml:"studio"`
	Actors        []nfoActor    `xml:"actor"`
	Plot          string        `xml:"plot"`
	TmdbID        string        `xml:"tmdbid"`
	IMDbID        string        `xml:"id"`
}

type nfoTVShow struct {
	XMLName       xml.Name      `xml:"tvshow"`
	Title         string        `xml:"title"`
	OriginalTitle string        `xml:"originaltitle"`
	Ratings       []nfoRating   `xml:"ratings>rating"`
	Year          string        `xml:"year"`
	Premiered     string        `xml:"premiered"`
	UniqueIDs     []nfoUniqueID `xml:"uniqueid"`
	Genres        []string      `xml:"genre"`
	Studios       []string      `xml:"studio"`
	Actors        []nfoActor    `xml:"actor"`
	Plot          string        `xml:"plot"`
	TmdbID        string        `xml:"tmdbid"`
}

func marshalNFO(v any) ([]byte, error) {
	b, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}

func writeMetaFile(dir, name string, content []byte, force bool) (bool, error) {
	dst := filepath.Join(dir, name)
	if !force {
		if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
			return false, nil // 已存在且不强制 → 跳过
		}
	}
	return true, os.WriteFile(dst, content, 0644)
}

// ---- 处理器 ----

// ScrapeGetConfig GET /scrape/config → 配置 + 状态
func (h *Handler) ScrapeGetConfig(c *gin.Context) {
	cfg := loadScrapeCfg()
	c.JSON(http.StatusOK, gin.H{"cfg": cfg, "status": scrapeStatusSnapshot()})
}

// ScrapeSaveConfig POST /scrape/config
func (h *Handler) ScrapeSaveConfig(c *gin.Context) {
	var req scrapeCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	req.LocalRoot = strings.TrimRight(strings.TrimSpace(req.LocalRoot), "/")
	if err := saveScrapeCfg(req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[影视刮削] ✓ 配置已保存（根目录 %s）", req.LocalRoot)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ScrapeRun POST /scrape/run → 后台刮削（进行中时拒绝重复触发）
func (h *Handler) ScrapeRun(c *gin.Context) {
	scrapeMu.Lock()
	if scrapeSt.Running {
		scrapeMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "刮削正在进行中"})
		return
	}
	scrapeSt = scrapeStatus{Running: true, Errors: []string{}}
	scrapeMu.Unlock()
	go h.scrapeAll(loadScrapeCfg())
	c.JSON(http.StatusOK, gin.H{"message": "刮削已开始，可刷新状态查看进度"})
}

// ScrapeStatus GET /scrape/status
func (h *Handler) ScrapeStatus(c *gin.Context) {
	c.JSON(http.StatusOK, scrapeStatusSnapshot())
}

// ScrapeStop POST /scrape/stop → 停止本轮（当前条目处理完即退出）
func (h *Handler) ScrapeStop(c *gin.Context) {
	scrapeMu.Lock()
	if scrapeSt.Running {
		scrapeStopFlag = true
	}
	scrapeMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"message": "已请求停止，当前条目处理完即退出"})
}

func scrapeStopRequested() bool {
	select {
	case <-stopCh:
		return true
	default:
	}
	scrapeMu.Lock()
	defer scrapeMu.Unlock()
	return scrapeStopFlag
}

// scrapeAll 主流程：遍历媒体库（有 tmdb id 的）逐条刮削
func (h *Handler) scrapeAll(cfg scrapeCfg) {
	defer func() {
		scrapeMu.Lock()
		scrapeSt.Running = false
		scrapeSt.Current = ""
		scrapeMu.Unlock()
		log.Printf("[影视刮削] ■ 本轮结束：完成 %d，失败 %d", scrapeStatusSnapshot().Done, scrapeStatusSnapshot().Failed)
	}()
	if cfg.LocalRoot == "" {
		scrapeAddErr("未配置本地媒体库根目录")
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		scrapeAddErr("TMDB 未配置: %v", err)
		return
	}
	var mls []model.MediaLibrary
	if err := model.DB.Where("tmdb_id <> 0 AND target_path <> ''").Find(&mls).Error; err != nil {
		scrapeAddErr("读取媒体库失败: %v", err)
		return
	}
	scrapeMu.Lock()
	scrapeSt.Total = len(mls)
	scrapeMu.Unlock()
	if len(mls) == 0 {
		return
	}
	log.Printf("[影视刮削] ▶ 开始：共 %d 个片目（根目录 %s）", len(mls), cfg.LocalRoot)
	for _, m := range mls {
		if scrapeStopRequested() {
			return
		}
		scrapeMu.Lock()
		scrapeSt.Current = m.Title
		scrapeMu.Unlock()
		dir := filepath.Join(cfg.LocalRoot, filepath.FromSlash(strings.Trim(m.TargetPath, "/")))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			scrapeAddErr("%s: 创建目录失败 %v", m.Title, err)
			scrapeMu.Lock()
			scrapeSt.Done++
			scrapeMu.Unlock()
			continue
		}
		h.scrapeOne(tc, cfg, dir, &m)
		scrapeMu.Lock()
		scrapeSt.Done++
		scrapeMu.Unlock()
		time.Sleep(150 * time.Millisecond) // TMDB 限速保护
	}
}

// scrapeOne 单个片目：详情 → NFO + 图片
func (h *Handler) scrapeOne(tc *TmdbClient, cfg scrapeCfg, dir string, m *model.MediaLibrary) {
	kindPath := "movie"
	if m.MediaType == "tv" {
		kindPath = "tv"
	}
	params := map[string]string{"language": "zh-CN", "append_to_response": "credits"}
	body, err := tc.get("/"+kindPath+"/"+strconv.Itoa(int(m.TmdbID)), params)
	if err != nil {
		scrapeAddErr("%s: TMDB 详情失败 %v", m.Title, err)
		return
	}

	// ---- NFO ----
	if cfg.WriteNFO {
		if m.MediaType == "movie" {
			var d struct {
				Title         string  `json:"title"`
				OriginalTitle string  `json:"original_title"`
				Overview      string  `json:"overview"`
				VoteAverage   float64 `json:"vote_average"`
				ReleaseDate   string  `json:"release_date"`
				Runtime       int     `json:"runtime"`
				IMDbID        string  `json:"imdb_id"`
				Genres        []struct {
					Name string `json:"name"`
				} `json:"genres"`
				Credits struct {
					Cast []struct {
						Name      string `json:"name"`
						Character string `json:"character"`
					} `json:"cast"`
					Crew []struct {
						Job  string `json:"job"`
						Name string `json:"name"`
					} `json:"crew"`
				} `json:"credits"`
				ProductionCompanies []struct {
					Name string `json:"name"`
				} `json:"production_companies"`
			}
			if json.Unmarshal(body, &d) != nil {
				scrapeAddErr("%s: 详情解析失败", m.Title)
				return
			}
			nfo := nfoMovie{
				Title: d.Title, OriginalTitle: d.OriginalTitle, Plot: d.Overview,
				Ratings: []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: d.VoteAverage}},
				Year:    dateYear(d.ReleaseDate), Premiered: d.ReleaseDate,
				Runtime: d.Runtime,
				UniqueIDs: []nfoUniqueID{
					{Type: "tmdb", Default: true, Value: strconv.Itoa(int(m.TmdbID))},
					{Type: "imdb", Value: d.IMDbID},
				},
				TmdbID: strconv.Itoa(int(m.TmdbID)), IMDbID: d.IMDbID,
			}
			for _, g := range d.Genres {
				nfo.Genres = append(nfo.Genres, g.Name)
			}
			for _, c := range d.Credits.Crew {
				if c.Job == "Director" {
					nfo.Directors = append(nfo.Directors, c.Name)
				}
			}
			for _, cc := range d.Credits.Cast {
				if cc.Name == "" {
					continue
				}
				nfo.Actors = append(nfo.Actors, nfoActor{Name: cc.Name, Role: cc.Character})
			}
			for _, pc := range d.ProductionCompanies {
				nfo.Studios = append(nfo.Studios, pc.Name)
			}
			if b, err := marshalNFO(nfo); err == nil {
				if _, err := writeMetaFile(dir, "movie.nfo", b, cfg.Force); err != nil {
					scrapeAddErr("%s: 写 movie.nfo 失败 %v", m.Title, err)
				}
			}
		} else {
			var d struct {
				Name         string  `json:"name"`
				OriginalName string  `json:"original_name"`
				Overview     string  `json:"overview"`
				VoteAverage  float64 `json:"vote_average"`
				FirstAirDate string  `json:"first_air_date"`
				Genres       []struct {
					Name string `json:"name"`
				} `json:"genres"`
				Networks []struct {
					Name string `json:"name"`
				} `json:"networks"`
				CreatedBy []struct {
					Name string `json:"name"`
				} `json:"created_by"`
			}
			if json.Unmarshal(body, &d) != nil {
				scrapeAddErr("%s: 详情解析失败", m.Title)
				return
			}
			nfo := nfoTVShow{
				Title: d.Name, OriginalTitle: d.OriginalName, Plot: d.Overview,
				Ratings: []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: d.VoteAverage}},
				Year:    dateYear(d.FirstAirDate), Premiered: d.FirstAirDate,
				UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(int(m.TmdbID))}},
				TmdbID:    strconv.Itoa(int(m.TmdbID)),
			}
			for _, g := range d.Genres {
				nfo.Genres = append(nfo.Genres, g.Name)
			}
			for _, nw := range d.Networks {
				nfo.Studios = append(nfo.Studios, nw.Name)
			}
			for _, cb := range d.CreatedBy {
				nfo.Actors = append(nfo.Actors, nfoActor{Name: cb.Name})
			}
			if b, err := marshalNFO(nfo); err == nil {
				if _, err := writeMetaFile(dir, "tvshow.nfo", b, cfg.Force); err != nil {
					scrapeAddErr("%s: 写 tvshow.nfo 失败 %v", m.Title, err)
				}
			}
		}
	}

	// ---- 图片 ----
	if !cfg.WriteImages {
		return
	}
	var d struct {
		PosterPath   string `json:"poster_path"`
		BackdropPath string `json:"backdrop_path"`
		Seasons      []struct {
			SeasonNumber int    `json:"season_number"`
			PosterPath   string `json:"poster_path"`
		} `json:"seasons"`
	}
	if json.Unmarshal(body, &d) != nil {
		return
	}
	images := [][2]string{
		{d.PosterPath, "poster.jpg"},
		{d.BackdropPath, "fanart.jpg"},
	}
	if m.MediaType == "tv" {
		for _, sn := range d.Seasons {
			if sn.PosterPath == "" {
				continue
			}
			name := fmt.Sprintf("season%02d-poster.jpg", sn.SeasonNumber)
			images = append(images, [2]string{sn.PosterPath, name})
		}
	}
	for _, img := range images {
		if scrapeStopRequested() {
			return
		}
		if img[0] == "" {
			continue
		}
		data, err := tmdbFetchImageBytes(img[0])
		if err != nil {
			scrapeAddErr("%s: 拉图失败 %s %v", m.Title, img[1], err)
			continue
		}
		if _, err := writeMetaFile(dir, img[1], data, cfg.Force); err != nil {
			scrapeAddErr("%s: 写 %s 失败 %v", m.Title, img[1], err)
		}
	}
}

func dateYear(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return d
}
