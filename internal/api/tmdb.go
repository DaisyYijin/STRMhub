package api

import (
	"encoding/json"
	"bytes"
	"fmt"
	"log"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

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
		log.Printf("[整理] 搜索 %q（年份=%s）无结果", query, year)
		return nil, nil
	}
	// 候选列表（CMS 同款：识别错片时可从候选看出原因）
	cands := make([]string, 0, 3)
	for _, c := range result.Results {
		if len(cands) >= 3 {
			break
		}
		cy := ""
		if len(c.ReleaseDate) >= 4 {
			cy = c.ReleaseDate[:4]
		}
		cands = append(cands, fmt.Sprintf("%s (%s) tmdb=%d 评分%.1f", c.Title, cy, c.ID, c.VoteAverage))
	}
	log.Printf("[整理] 搜索 %q 候选 %d 个: %s", query, result.TotalResults, strings.Join(cands, " | "))
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
func (tc *TmdbClient) SearchTV(query string, year string) (*TmdbMedia, error) {
	params := map[string]string{"query": query}
	if year != "" {
		params["first_air_date_year"] = year // TMDB 剧集搜索的年份参数
	}
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
		log.Printf("[整理] 搜索 %q（TV，年份=%s）无结果", query, year)
		return nil, nil
	}
	cands := make([]string, 0, 3)
	for _, c := range result.Results {
		if len(cands) >= 3 {
			break
		}
		cy := ""
		if len(c.FirstAirDate) >= 4 {
			cy = c.FirstAirDate[:4]
		}
		cands = append(cands, fmt.Sprintf("%s (%s) tmdb=%d 评分%.1f", c.Name, cy, c.ID, c.VoteAverage))
	}
	log.Printf("[整理] 搜索 %q（TV）候选 %d 个: %s", query, result.TotalResults, strings.Join(cands, " | "))
	r := result.Results[0]
	resultYear := ""
	if len(r.FirstAirDate) >= 4 {
		resultYear = r.FirstAirDate[:4]
	}
	origCountry, _ := tc.getTVDetails(r.ID)
	return &TmdbMedia{
		TmdbID:       r.ID,
		Title:        r.Name,
		OriginalTitle: r.OriginalName,
		Year:         resultYear,
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
	Quality    string // 完整画质串（1080p.WEB-DL.AAC2.0.H.264 等，由调用方填充）
}

var (
	// 季集模式：S01E02, s01e02, 1x02
	reSeasonEpisode = regexp.MustCompile(`[Ss](\d{1,2})[Ee](\d{1,3})`)
	// 仅集数模式：EP01 / E01 / 第01集（无季信息，季缺省为 1）
	// 前后必须有分隔符或边界，避免误吃 "WALL.E.2008" 这类片名
	reEpisodeOnly = regexp.MustCompile(`(?:^|[\.\s_-])(?:[Ee][Pp]?|第)(\d{1,3})(?:[集話话])?(?:$|[\.\s_-])`)
	// 动漫字幕组命名的方括号集数：[01] / [01v2]（vN=修正版）。
	// 限 1-3 位数字：4 位会被 "[2001]" 这类年份方括号误伤
	reBracketEpisode = regexp.MustCompile(`(?:^|[\.\s_-])\[(\d{1,3})(?:[vV]\d+)?\]`)
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

// reAdBracketBlock / reAdDomain 发布站广告：全角括号块与域名
var (
	reAdBracketBlock = regexp.MustCompile(`【[^【】]*】`)
	reAdDomain       = regexp.MustCompile(`(?i)\b(www\.)?[a-z0-9][a-z0-9-]{1,15}\.(com|net|org|cc|xyz|me|tv|info|vip|top)\b`)
)

// stripReleaseAds 剥离文件名/目录名中的发布站广告（【高清影视之家发布
// www.SSDSSE.com】块与裸域名），清理残留分隔符。放在 parseFileName 最前，
// 保证标题提取和搜索都不被广告前缀污染
func stripReleaseAds(name string) string {
	name = reAdBracketBlock.ReplaceAllString(name, " ")
	name = reAdDomain.ReplaceAllString(name, " ")
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return strings.Trim(name, " -_.@")
}

// parseFileName 从视频文件名解析标题、年份、季集等信息
func parseFileName(filename string) *ParsedName {
	// 去掉文件后缀
	name := filename
	if idx := strings.LastIndex(name, "."); idx > 0 {
		name = name[:idx]
	}
	// 先剥离发布站广告（【…】块/域名），再解析
	name = stripReleaseAds(name)

	result := &ParsedName{}

	// 检测季集
	if m := reSeasonEpisode.FindStringSubmatch(name); m != nil {
		result.IsTV = true
		result.Season, _ = strconv.Atoi(m[1])
		result.Episode, _ = strconv.Atoi(m[2])
	} else if m := reEpisodeOnly.FindStringSubmatch(name); m != nil {
		// EP01 / E01 / 第01集：无季信息，缺省第 1 季
		result.IsTV = true
		result.Season = 1
		result.Episode, _ = strconv.Atoi(m[1])
	} else if m := reBracketEpisode.FindStringSubmatch(name); m != nil {
		// 动漫字幕组命名 [01] / [01v2]：无季信息，缺省第 1 季。
		// 同时剥离开头的 [字幕组] 前缀——仅在确认是这种命名形态时才剥，
		// 防止误伤 "[REC].2007" 这类以方括号开头的电影名
		result.IsTV = true
		result.Season = 1
		result.Episode, _ = strconv.Atoi(m[1])
		name = regexp.MustCompile(`^\[[^\]]*\]\s*`).ReplaceAllString(name, "")
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
	// 在仅集数标记（EP01/E01/第01集）处截断，避免集号污染标题搜索
	if idx := reEpisodeOnly.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}
	// 在方括号集数（[01]/[01v2]）处截断，同时去掉后面的编码/语言标签
	if idx := reBracketEpisode.FindStringIndex(title); idx != nil {
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

	// movieThenTV：先电影后剧集（CMS 同款兜底顺序）
	movieThenTV := func(q string) (*TmdbMedia, error) {
		media, err := tc.SearchMovie(q, parsed.Year)
		if err != nil {
			return nil, err
		}
		if media != nil {
			return media, nil
		}
		return tc.SearchTV(q, parsed.Year)
	}

	// 第一轮：原始标题（剧集直接搜 TV）
	var media *TmdbMedia
	var err error
	if parsed.IsTV {
		media, err = tc.SearchTV(parsed.Title, parsed.Year)
	} else {
		media, err = movieThenTV(parsed.Title)
	}
	if err != nil || media != nil {
		return media, err
	}

	// 第 1.5 轮：年份搜索无结果 → 去掉年份宽搜索，按年份最近匹配
	// 场景：文件名写 2018 但 TMDB 记为 2017（跨年上映/首播）
	if parsed.Year != "" {
		var wide *TmdbMedia
		if parsed.IsTV {
			wide, err = tc.SearchTV(parsed.Title, "")
		} else {
			wide, err = tc.SearchMovie(parsed.Title, "")
		}
		if err == nil && wide != nil {
			// 如果唯一结果或年份差 ≤1 年，接受
			yearDiff := absYearDiff(wide.Year, parsed.Year)
			if yearDiff <= 1 {
				log.Printf("[整理] 年份宽搜索命中: %q 年份=%s（文件名=%s，差 %d 年）",
					wide.Title, wide.Year, parsed.Year, yearDiff)
				return wide, nil
			}
			// 如果多个结果，尝试找年份最接近的（在 SearchXxx 内已取第一个，
			// 这里只处理唯一结果的情况；多结果的精确匹配需要改 SearchXxx 返回列表）
			log.Printf("[整理] 年份宽搜索: 找到 %q 年份=%s，与文件名 %s 差 %d 年，跳过",
				wide.Title, wide.Year, parsed.Year, yearDiff)
		}
	}

	// 第二轮：清洗后的标题重试（去掉特殊字符/残留标记，压紧空白）
	cleaned := cleanSearchTitle(parsed.Title)
	if cleaned != "" && cleaned != parsed.Title {
		log.Printf("[整理] 首次搜索无结果，用清洗标题重试: %q → %q", parsed.Title, cleaned)
		if parsed.IsTV {
			media, err = tc.SearchTV(cleaned, parsed.Year)
		} else {
			media, err = movieThenTV(cleaned)
		}
		if err != nil || media != nil {
			return media, err
		}
	}

	// 第二点五轮：中英混合标题拆分搜索。
	// 场景："骗不了人的男人 Softie Conman"——TMDB 对混合串整体匹配不到，
	// 中文名或英文名单独搜索才能命中
	if cjk, latin := splitCJKLatin(parsed.Title); cjk != "" && latin != "" {
		for _, q := range []string{cjk, latin} {
			log.Printf("[整理] 混合标题拆分搜索: %q（原 %q）", q, parsed.Title)
			if parsed.IsTV {
				media, err = tc.SearchTV(q, parsed.Year)
			} else {
				media, err = movieThenTV(q)
			}
			if err != nil || media != nil {
				return media, err
			}
		}
	}

	// 第三轮：GPT 兜底（配置了 GPT 识别时）——从原始文件名提取标题/年份再搜
	if gptCfg := loadGPTFallback(); gptCfg != nil {
		if ext := gptExtract(gptCfg, parsed.Title); ext != nil && ext.Title != "" && ext.Title != parsed.Title {
			log.Printf("[整理] GPT 兜底提取: %q → %q (%s)", parsed.Title, ext.Title, ext.Year)
			p2 := *parsed
			p2.Title = ext.Title
			p2.Year = ext.Year
			if p2.Year == "" {
				p2.Year = parsed.Year
			}
			if parsed.IsTV {
				return tc.SearchTV(p2.Title, parsed.Year)
			}
			return movieThenTV(p2.Title)
		}
	}
	return media, nil
}

// splitCJKLatin 把中英混合标题拆成中文名与英文名（各自取最长连续段）。
// "骗不了人的男人[国日多音轨+中文字幕] Softie Conman"
//   → ("骗不了人的男人", "Softie Conman")
func splitCJKLatin(title string) (cjk, latin string) {
	isCJK := func(r rune) bool {
		return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
	}
	isLatin := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '\'' || r == '’' || r == ':' || r == '!' || r == '-' || r == '&' || r == '.'
	}
	var curCJK, curLatin, bestCJK, bestLatin []rune
	flush := func() {
		if len(curCJK) > len(bestCJK) {
			bestCJK = append([]rune(nil), curCJK...)
		}
		if len(curLatin) > len(bestLatin) {
			bestLatin = append([]rune(nil), curLatin...)
		}
		curCJK, curLatin = curCJK[:0], curLatin[:0]
	}
	for _, r := range title {
		switch {
		case isCJK(r):
			if len(curLatin) > 0 {
				flush()
			}
			curCJK = append(curCJK, r)
		case isLatin(r):
			if len(curCJK) > 0 {
				flush()
			}
			curLatin = append(curLatin, r)
		case r == ' ' || r == '·':
			// 空格归入当前段，不打断连续性
			if len(curCJK) > 0 {
				curCJK = append(curCJK, r)
			} else if len(curLatin) > 0 {
				curLatin = append(curLatin, r)
			}
		default:
			flush()
		}
	}
	flush()
	cjk = strings.TrimSpace(string(bestCJK))
	latin = strings.TrimSpace(string(bestLatin))
	// 拉丁段必须含字母（排除 "2022"/"1080p" 这类纯数字残留）
	if !strings.ContainsFunc(latin, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) {
		latin = ""
	}
	return cjk, latin
}

// gptFallbackCfg GPT 识别配置（org-gpt 设置）
type gptFallbackCfg struct {
	URL   string
	Key   string
	Model string
}

var gptFallbackCfgCache struct {
	val *gptFallbackCfg
	at  time.Time
}

// loadGPTFallback 读取 GPT 兜底配置（5 分钟缓存；未配置返回 nil）
func loadGPTFallback() *gptFallbackCfg {
	if gptFallbackCfgCache.val != nil && time.Since(gptFallbackCfgCache.at) < 5*time.Minute {
		return gptFallbackCfgCache.val
	}
	v := ""
	var sRow model.Setting
	if err := model.DB.Where("`key` = ?", "org-gpt").First(&sRow).Error; err == nil {
		v = sRow.Value
	}
	var cfg struct {
		URL   string `json:"url"`
		Key   string `json:"key"`
		Model string `json:"model"`
	}
	gptFallbackCfgCache.val = nil
	if v != "" && json.Unmarshal([]byte(v), &cfg) == nil && cfg.URL != "" && cfg.Key != "" {
		if cfg.Model == "" {
			cfg.Model = "gpt-4o-mini"
		}
		gptFallbackCfgCache.val = &gptFallbackCfg{URL: cfg.URL, Key: cfg.Key, Model: cfg.Model}
	}
	gptFallbackCfgCache.at = time.Now()
	return gptFallbackCfgCache.val
}

// gptExtract 用 GPT 从文件名提取 标题/年份
type gptExtractResult struct {
	Title string
	Year  string
}

func gptExtract(cfg *gptFallbackCfg, filename string) *gptExtractResult {
	if filename == "" {
		return nil
	}
	payload := map[string]interface{}{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "从影视文件名中提取标准标题和上映/开播年份。只输出 JSON：{\"title\":\"...\",\"year\":\"...\"}，找不到年份输出空字符串。"},
			{"role": "user", "content": filename},
		},
		"temperature": 0,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.URL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Key)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[整理] 调用失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("[整理] HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 100))
		return nil
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Choices) == 0 {
		return nil
	}
	content := r.Choices[0].Message.Content
	m := regexp.MustCompile(`\{[^}]*\}`).FindString(content)
	if m == "" {
		return nil
	}
	var out gptExtractResult
	if json.Unmarshal([]byte(m), &out) != nil || out.Title == "" {
		return nil
	}
	return &out
}

// cleanSearchTitle 清洗搜索标题：仅保留中文/字母/数字/空格，压紧空白
func cleanSearchTitle(title string) string {
	var b []rune
	for _, r := range title {
		switch {
		case r >= 0x4e00 && r <= 0x9fff, // 汉字
			r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, r)
		case r == ' ' || r == '　' || r == '.' || r == '-' || r == '_' || r == ':' || r == '：':
			b = append(b, ' ')
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(string(b)), " "))
}

// absYearDiff 计算两个年份字符串的绝对差值（解析失败返回 999）
func absYearDiff(a, b string) int {
	ai, err1 := strconv.Atoi(strings.TrimSpace(a))
	bi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return 999
	}
	d := ai - bi
	if d < 0 {
		return -d
	}
	return d
}
