package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strmhub/internal/model"
)

// ==================== TMDB 客户端 ====================

// TmdbClient 封装 TMDB API 调用
type TmdbClient struct {
	APIKey     string
	APIURL     string
	ImageURL   string
	Language   string
	ProxyURL   string
	httpClient *http.Client
}

// TmdbMedia TMDB 识别结果
type TmdbMedia struct {
	TmdbID      int                `json:"tmdb_id"`
	Title       string             `json:"title"`
	OriginalTitle string           `json:"original_title"`
	Year        string             `json:"year"`
	MediaType   string             `json:"media_type"` // movie, tv
	GenreIDs    []int              `json:"genre_ids"`
	Overview    string             `json:"overview"`
	PosterPath  string             `json:"poster_path"`
	BackdropPath string           `json:"backdrop_path"`
	OrigLanguage string           `json:"original_language"`
	OrigCountry []string          `json:"origin_country"`
	VoteAverage float64           `json:"vote_average"`
	// TV 专属
	SeasonNum   int                `json:"season_number,omitempty"`
	EpisodeNum  int                `json:"episode_number,omitempty"`
}

// loadTmdbClient 从数据库加载配置构建客户端
func loadTmdbClient(db interface{ Where(query interface{}, args ...interface{}) interface{ First(dest interface{}) interface{} } }) (*TmdbClient, error) {
	_ = db
	// 通过全局 DB 读取
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("TMDB 未配置")
	}
	if cfg.ApiKey == "" {
		return nil, fmt.Errorf("TMDB API 密钥未填写")
	}
	tc := &TmdbClient{
		APIKey:   cfg.ApiKey,
		APIURL:   normalizeTMDBBase(cfg.ApiUrl),
		ImageURL: strings.TrimRight(cfg.ImageApiUrl, "/"),
		Language: cfg.Language,
	}
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		tc.ProxyURL = cfg.ProxyUrl
	}
	tc.httpClient = &http.Client{Timeout: 15 * time.Second}
	return tc, nil
}

// normalizeTMDBBase 规范化 TMDB API 地址：去尾斜杠、补 /3 版本前缀
// TMDB 所有接口都在 /3 下（如 /3/search/movie），漏写前缀会 404
func normalizeTMDBBase(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return "https://api.themoviedb.org/3"
	}
	if !strings.HasSuffix(u, "/3") {
		u += "/3"
	}
	return u
}

// get 发送 GET 请求到 TMDB API
func (tc *TmdbClient) get(endpoint string, params map[string]string) ([]byte, error) {
	u := tc.APIURL + endpoint
	v := url.Values{}
	v.Set("api_key", tc.APIKey)
	v.Set("language", tc.Language)
	for k, val := range params {
		v.Set(k, val)
	}
	fullURL := u + "?" + v.Encode()

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := tc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("TMDB HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// SearchMovie 搜索电影
func (tc *TmdbClient) SearchMovie(query string, year string) (*TmdbMedia, error) {
	params := map[string]string{"query": query}
	if year != "" {
		params["year"] = year
	}
	body, err := tc.get("/search/movie", params)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []struct {
			ID               int     `json:"id"`
			Title            string  `json:"title"`
			OriginalTitle    string  `json:"original_title"`
			ReleaseDate      string  `json:"release_date"`
			GenreIDs         []int   `json:"genre_ids"`
			Overview         string  `json:"overview"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			OriginalLanguage string  `json:"original_language"`
			VoteAverage      float64 `json:"vote_average"`
		} `json:"results"`
		TotalResults int `json:"total_results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.TotalResults == 0 || len(result.Results) == 0 {
		return nil, nil
	}
	r := result.Results[0]
	yr := ""
	if len(r.ReleaseDate) >= 4 {
		yr = r.ReleaseDate[:4]
	}
	// 获取详情中的 origin_country
	origCountry, _ := tc.getMovieDetails(r.ID)
	return &TmdbMedia{
		TmdbID:       r.ID,
		Title:        r.Title,
		OriginalTitle: r.OriginalTitle,
		Year:         yr,
		MediaType:    "movie",
		GenreIDs:     r.GenreIDs,
		Overview:     r.Overview,
		PosterPath:   r.PosterPath,
		BackdropPath: r.BackdropPath,
		OrigLanguage: r.OriginalLanguage,
		OrigCountry:  origCountry,
		VoteAverage:  r.VoteAverage,
	}, nil
}

// getMovieDetails 获取电影详情（origin_country）
func (tc *TmdbClient) getMovieDetails(id int) ([]string, error) {
	body, err := tc.get(fmt.Sprintf("/movie/%d", id), nil)
	if err != nil {
		return nil, err
	}
	var detail struct {
		OriginCountry []string `json:"origin_country"`
	}
	json.Unmarshal(body, &detail)
	return detail.OriginCountry, nil
}

// SearchTV 搜索电视剧
func (tc *TmdbClient) SearchTV(query string) (*TmdbMedia, error) {
	params := map[string]string{"query": query}
	body, err := tc.get("/search/tv", params)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []struct {
			ID               int     `json:"id"`
			Name             string  `json:"name"`
			OriginalName     string  `json:"original_name"`
			FirstAirDate     string  `json:"first_air_date"`
			GenreIDs         []int   `json:"genre_ids"`
			Overview         string  `json:"overview"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			OriginalLanguage string  `json:"original_language"`
			VoteAverage      float64 `json:"vote_average"`
		} `json:"results"`
		TotalResults int `json:"total_results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.TotalResults == 0 || len(result.Results) == 0 {
		return nil, nil
	}
	r := result.Results[0]
	year := ""
	if len(r.FirstAirDate) >= 4 {
		year = r.FirstAirDate[:4]
	}
	origCountry, _ := tc.getTVDetails(r.ID)
	return &TmdbMedia{
		TmdbID:       r.ID,
		Title:        r.Name,
		OriginalTitle: r.OriginalName,
		Year:         year,
		MediaType:    "tv",
		GenreIDs:     r.GenreIDs,
		Overview:     r.Overview,
		PosterPath:   r.PosterPath,
		BackdropPath: r.BackdropPath,
		OrigLanguage: r.OriginalLanguage,
		OrigCountry:  origCountry,
		VoteAverage:  r.VoteAverage,
	}, nil
}

// getTVDetails 获取电视剧详情（origin_country）
func (tc *TmdbClient) getTVDetails(id int) ([]string, error) {
	body, err := tc.get(fmt.Sprintf("/tv/%d", id), nil)
	if err != nil {
		return nil, err
	}
	var detail struct {
		OriginCountry []string `json:"origin_country"`
	}
	json.Unmarshal(body, &detail)
	return detail.OriginCountry, nil
}

// ==================== 文件名解析 ====================

// ParsedName 从文件名解析出的信息
type ParsedName struct {
	Title      string
	Year       string
	Season     int
	Episode    int
	IsTV       bool
	Resolution string // 1080p, 2160p 等
}

var (
	// 季集模式：S01E02, s01e02, 1x02
	reSeasonEpisode = regexp.MustCompile(`[Ss](\d{1,2})[Ee](\d{1,3})`)
	// 仅季：Season 1, 第一季
	reSeasonOnly = regexp.MustCompile(`[Ss](\d{1,2})\b`)
	// 年份：(2023) 或 .2023. 或空格2023空格
	reYear = regexp.MustCompile(`[\(\.\s_](19\d{2}|20\d{2})[\)\.\s_]`)
	// 分辨率
	reResolution = regexp.MustCompile(`(?i)(4K|2160P|1080[PI]|720P|480P)`)
	// 发布组常见标记（用于截断标题）
	reReleaseMarkers = regexp.MustCompile(`(?i)[\.\s_-](BluRay|BDRip|BRRip|DVDRip|WEBRip|WEB-DL|HDTV|REMUX|CAM|TS|TC|R5|HDRip|HC|HQ|PROPER|REPACK|iNTERNAL|LIMITED|UNRATED|DC|EXTENDED|UNCUT|DUBBED|SUBBED|DUAL|MULTi|MULTIAUDIO|RETAIL|COMPLETE|FINAL|REMASTERED|IMAX|3D|HSBS|HOU|DOVi|Dolby|Atmos|TrueHD|DTS|DDP|DD\+?|AAC|AC3|x264|x265|h264|h265|AVC|HEVC|10bit|SDR|HDR|\d{3,4}p|10-Bit)`)
	// 发布组后缀（-GROUP）
	reReleaseGroup = regexp.MustCompile(`[\.\s_-]([A-Za-z0-9]+)$`)
)

// parseFileName 从视频文件名解析标题、年份、季集等信息
func parseFileName(filename string) *ParsedName {
	// 去掉文件后缀
	name := filename
	if idx := strings.LastIndex(name, "."); idx > 0 {
		name = name[:idx]
	}

	result := &ParsedName{}

	// 检测季集
	if m := reSeasonEpisode.FindStringSubmatch(name); m != nil {
		result.IsTV = true
		result.Season, _ = strconv.Atoi(m[1])
		result.Episode, _ = strconv.Atoi(m[2])
	} else if m := reSeasonOnly.FindStringSubmatch(name); m != nil {
		result.IsTV = true
		result.Season, _ = strconv.Atoi(m[1])
	}

	// 检测分辨率
	if m := reResolution.FindStringSubmatch(name); m != nil {
		result.Resolution = strings.ToUpper(m[1])
	}

	// 检测年份
	if m := reYear.FindStringSubmatch(name); m != nil {
		result.Year = m[1]
	}

	// 提取标题：从开头到第一个发布标记/季集/年份处截断
	title := name

	// 将分隔符 . _ 替换为空格
	title = strings.ReplaceAll(title, ".", " ")
	title = strings.ReplaceAll(title, "_", " ")

	// 在季集标记处截断
	if idx := reSeasonEpisode.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}
	if !result.IsTV {
		if idx := reSeasonOnly.FindStringIndex(title); idx != nil {
			title = title[:idx[0]]
		}
	}

	// 在发布标记处截断
	if idx := reReleaseMarkers.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}

	// 在年份处截断
	if result.Year != "" {
		if idx := strings.Index(title, result.Year); idx > 0 {
			title = title[:idx]
		}
	}

	// 清理首尾空格和标点
	title = strings.Trim(title, " -.")
	// 合并多个空格
	title = regexp.MustCompile(`\s+`).ReplaceAllString(title, " ")

	result.Title = title
	return result
}

// recognizeFile 通过 TMDB 识别文件
func (tc *TmdbClient) recognize(parsed *ParsedName) (*TmdbMedia, error) {
	if parsed.Title == "" {
		return nil, fmt.Errorf("无法从文件名提取标题")
	}

	// 先尝试按文件类型判断
	if parsed.IsTV {
		return tc.SearchTV(parsed.Title)
	}

	// 电影：先搜索，如果没结果再尝试 TV
	media, err := tc.SearchMovie(parsed.Title, parsed.Year)
	if err != nil {
		return nil, err
	}
	if media != nil {
		return media, nil
	}

	// 电影没找到，尝试 TV
	return tc.SearchTV(parsed.Title)
}
