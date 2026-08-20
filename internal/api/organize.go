package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"

	"github.com/mozillazg/go-pinyin"
)

// ==================== 整理引擎 ====================

// OrganizeResult 整理单个文件的结果
type OrganizeResult struct {
	FileName   string `json:"file_name"`
	Status     string `json:"status"`      // success, skipped, failed, exists
	TmdbID     int    `json:"tmdb_id"`
	Title      string `json:"title"`
	Year       string `json:"year"`
	MediaType  string `json:"media_type"` // movie, tv
	Category   string `json:"category"`
	TargetDir  string `json:"target_dir"`
	Message    string `json:"message"`
}

// OrgConfig 整理配置（从数据库加载）
type OrgConfig struct {
	Pending     string `json:"pending"`    // 待整理目录 cid
	Library     string `json:"library"`    // 我的影视库 cid（整理后最终归宿）
	Existing    string `json:"existing"`   // 已存在目录 cid（洗版重复）
	Redundant   string `json:"redundant"`  // 冗余目录 cid（识别失败等）
	ReplaceRules string `json:"replace_rules"`
	MinSize     int64  `json:"min_size"`
}

// renameTpl 全局重命名模板（runOrganizeEngine 初始化时从配置加载）
var renameTpl *RenameConfig

// RenameConfig 重命名配置
type RenameConfig struct {
	MovieFolder string `json:"movie_folder"` // 电影文件夹命名规则
	MovieFile   string `json:"movie_file"`   // 电影文件命名规则
	TVFolder    string `json:"tv_folder"`    // 电视剧文件夹命名规则
	TVFile      string `json:"tv_file"`      // 电视剧文件命名规则
	AVFolder    string `json:"av_folder"`    // AV 文件夹命名规则
	AVFile      string `json:"av_file"`      // AV 文件命名规则
}

// loadOrgConfig 从数据库加载整理配置
// loadOrgConfig 加载整理配置（yaml 优先 DB 回退）；
// 影视库不再单独配置，直接使用全量同步配置的媒体库 cid
func (h *Handler) loadOrgConfig() (*OrgConfig, error) {
	var cfg OrgConfig
	if v := h.getSettingValue("org-basic"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	if cfg.Pending == "" {
		return nil, fmt.Errorf("未配置待整理文件夹")
	}
	if cfg.Existing == "" {
		return nil, fmt.Errorf("未配置已存在文件夹")
	}
	if cfg.Redundant == "" {
		return nil, fmt.Errorf("未配置冗余文件夹")
	}
	// 影视库 = 全量同步配置的媒体库 cid；全量未配置时兼容旧的 org-basic.library
	var fullCfg struct {
		Cid string `json:"cid"`
	}
	if v := h.getSettingValue("full"); v != "" {
		json.Unmarshal([]byte(v), &fullCfg)
	}
	if fullCfg.Cid != "" {
		cfg.Library = fullCfg.Cid
	}
	if cfg.Library == "" {
		return nil, fmt.Errorf("未配置全量同步的媒体库目录（整理目标库取自全量同步配置）")
	}
	return &cfg, nil
}

// orgAttachmentExts 整理时随视频同行的附件后缀（字幕/元数据/图片）
var orgAttachmentExts = map[string]bool{
	".ass": true, ".srt": true, ".ssa": true, ".sub": true, ".vtt": true, ".smi": true,
	".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// moveSiblingAttachments 把与视频同目录的附件（字幕/nfo/图片）随视频一起移动，
// 并把与视频同名的字幕重命名为视频新名（播放器按视频名匹配外挂字幕）
func moveSiblingAttachments(ops *pan115Ops, pendingCid, videoOldBase, videoNewBase, targetCid string, rename bool, onLog func(string)) {
	if pendingCid == "" {
		return
	}
	entries, _, err := ops.listEntries(pendingCid, 0)
	if err != nil {
		onLog(fmt.Sprintf("✗ 收集随行附件失败: %v", err))
		return
	}
	moved := 0
	for _, e := range entries {
		if fmt.Sprint(e["f"]) != "1" { // 只看文件
			continue
		}
		name := fmt.Sprint(e["n"])
		ext := strings.ToLower(pathExt(name))
		if !orgAttachmentExts[ext] {
			continue
		}
		fid := fmt.Sprint(e["fid"])
		base := baseName(name)
		// 与视频同名的附件才随行（前缀匹配），其余字幕在多个视频混放时无法归属
		if !strings.HasPrefix(base, videoOldBase) {
			continue
		}
		if err := ops.moveFiles(targetCid, []string{fid}); err != nil {
			onLog(fmt.Sprintf("✗ 附件随行移动失败 %s: %v", name, err))
			continue
		}
		moved++
		// 重命名对齐视频新名（仅成功入库场景；冗余/已存在保持原名）
		if rename && videoNewBase != "" && base != videoNewBase {
			newName := videoNewBase + strings.TrimPrefix(base, videoOldBase) + ext
			if err := ops.rename(fid, newName); err != nil {
				onLog(fmt.Sprintf("○ 附件随行 %s（重命名失败保持原名: %v）", name, err))
			} else {
				onLog(fmt.Sprintf("✓ 附件随行 %s → %s", name, newName))
			}
		} else {
			onLog(fmt.Sprintf("✓ 附件随行 %s", name))
		}
	}
	if moved > 0 {
		time.Sleep(300 * time.Millisecond)
	}
}

// renameToStandard 视频按标准名重命名（入库后调用，按 fid 重命名）：
//   电影单文件 → 标题 (年份) [tmdb id].ext
//   剧集（可解析出 S/E）→ 标题 - S01E02.ext
//   无法解析集号的保留原名；同目录字幕的基名跟随所属视频新名
func renameToStandard(ops *pan115Ops, media *TmdbMedia, videoFiles, files []remoteFile, newPath string, onLog func(string)) {
	type vRen struct{ fid, oldBase, newBase, ext string }
	var renames []vRen
	stdBase := ""
	if media.MediaType == "movie" {
		// buildNewName 的文件段即标准名（去掉扩展名）
		stdBase = baseName(pathBase(newPath))
	}
	for _, vf := range videoFiles {
		ext := pathExt(vf.Name)
		oldBase := baseName(vf.Name)
		nb := ""
		if media.MediaType == "movie" && stdBase != "" && len(videoFiles) == 1 {
			nb = stdBase
		} else if media.MediaType == "tv" {
			if p := parseFileName(vf.Name); p.Season > 0 && p.Episode > 0 {
				nb = fmt.Sprintf("%s - S%02dE%02d", media.Title, p.Season, p.Episode)
			}
		}
		if nb == "" || nb == oldBase {
			continue
		}
		renames = append(renames, vRen{fid: vf.Fid, oldBase: oldBase, newBase: nb, ext: ext})
	}
	if len(renames) == 0 {
		return
	}
	// 视频重命名
	for _, r := range renames {
		if err := ops.rename(r.fid, r.newBase+r.ext); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", r.oldBase+r.ext, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s%s", r.oldBase, r.newBase, r.ext))
		}
	}
	// 字幕基名跟随所属视频（前缀匹配视频旧基名）
	for _, f := range files {
		ext := strings.ToLower(pathExt(f.Name))
		if !orgAttachmentExts[ext] {
			continue
		}
		fb := baseName(f.Name)
		for _, r := range renames {
			if fb == r.oldBase || strings.HasPrefix(fb, r.oldBase+".") {
				suffix := strings.TrimPrefix(fb, r.oldBase)
				if err := ops.rename(f.Fid, r.newBase+suffix+ext); err != nil {
					onLog(fmt.Sprintf("○ 字幕重命名失败保持原名 %s: %v", f.Name, err))
				} else {
					onLog(fmt.Sprintf("✓ 字幕重命名 %s → %s%s", f.Name, r.newBase, suffix+ext))
				}
				break
			}
		}
	}
}

// renameBeforeMove 在源目录中先重命名文件（带画质信息），再移动到目标
// 顺序：重命名 → 移动（而非 移动 → 重命名，减少目标目录的中间状态）
func renameBeforeMove(ops *pan115Ops, media *TmdbMedia, videoFiles, files []remoteFile, onLog func(string)) {
	for _, vf := range videoFiles {
		ext := pathExt(vf.Name)
		p := parseFileName(vf.Name)
		ri := ParseResourceInfo(vf.Name)
		quality := ri.QualityString()

		var newName string
		if media.MediaType == "movie" {
			newName = fmt.Sprintf("%s (%s) [%d]", media.Title, media.Year, media.TmdbID)
		} else if p.Season > 0 && p.Episode > 0 {
			newName = fmt.Sprintf("%s - S%02dE%02d", media.Title, p.Season, p.Episode)
		} else {
			continue
		}
		if quality != "" {
			newName += "." + quality
		}
		if ri.Team != "" {
			newName += "-" + ri.Team
		}
		newName += ext

		if newName != vf.Name {
			if err := ops.rename(vf.Fid, newName); err != nil {
				onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", vf.Name, err))
			} else {
				onLog(fmt.Sprintf("✓ 重命名 %s → %s", vf.Name, newName))
			}
		}
	}

	// 字幕跟随视频新名（前缀匹配）
	for _, f := range files {
		ext := strings.ToLower(pathExt(f.Name))
		if !orgAttachmentExts[ext] {
			continue
		}
		fb := baseName(f.Name)
		for _, vf := range videoFiles {
			vfBase := baseName(vf.Name)
			if fb == vfBase || strings.HasPrefix(fb, vfBase+".") {
				p := parseFileName(vf.Name)
				suffix := strings.TrimPrefix(fb, vfBase)
				var newSub string
				if media.MediaType == "movie" {
					newSub = fmt.Sprintf("%s (%s) [%d]", media.Title, media.Year, media.TmdbID)
				} else {
					newSub = fmt.Sprintf("%s - S%02dE%02d", media.Title, p.Season, p.Episode)
					if q := ParseResourceInfo(vf.Name).QualityString(); q != "" {
						newSub += "." + q
					}
				}
				newSubName := newSub + suffix + ext
				if newSubName != f.Name {
					if err := ops.rename(f.Fid, newSubName); err != nil {
						onLog(fmt.Sprintf("○ 字幕重命名失败 %s: %v", f.Name, err))
					} else {
						onLog(fmt.Sprintf("✓ 字幕 %s → %s", f.Name, newSubName))
					}
				}
				break
			}
		}
	}
}

// moveQuietly 移动并记录失败（失败不再被吞掉）
func moveQuietly(ops *pan115Ops, targetCid string, fids []string, label string, onLog func(string)) {
	if err := ops.moveFiles(targetCid, fids); err != nil {
		onLog(fmt.Sprintf("✗ %s - 移动失败: %v", label, err))
	}
}

// loadReplaceRules 加载替换规则
func loadReplaceRules() []ReplaceRule {
	var s model.Setting
	if err := model.DB.Where("key = ?", "org-recognize").First(&s).Error; err != nil {
		return nil
	}
	var cfg struct {
		ReplaceRules string `json:"replace_rules"`
	}
	json.Unmarshal([]byte(s.Value), &cfg)
	if cfg.ReplaceRules == "" {
		return nil
	}
	var rules []ReplaceRule
	json.Unmarshal([]byte(cfg.ReplaceRules), &rules)
	return rules
}

// ReplaceRule 替换规则
type ReplaceRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// applyReplaceRules 应用替换规则到文件名
func applyReplaceRules(name string, rules []ReplaceRule) string {
	for _, r := range rules {
		if r.From != "" {
			name = strings.ReplaceAll(name, r.From, r.To)
		}
	}
	return name
}

// mediaTypeCategory 媒体类型对应的一级分类目录名
func mediaTypeCategory(mediaType string) string {
	switch mediaType {
	case "movie":
		return "电影"
	case "tv":
		return "剧集"
	default:
		return "AV"
	}
}

// classifyMedia 根据分类规则判断二级分类
func classifyMedia(media *TmdbMedia) string {
	var categories []model.CategoryRule
	// 电影和电视剧分开查询
	mediaType := "movie"
	if media.MediaType == "tv" {
		mediaType = "tv"
	}
	model.DB.Where("media_type = ?", mediaType).Order("priority ASC").Find(&categories)

	for _, cat := range categories {
		if matchCategory(&cat, media) {
			return cat.Name
		}
	}

	// 查找默认分类
	for _, cat := range categories {
		if cat.IsDefault {
			return cat.Name
		}
	}

	return "未分类"
}

// matchCategory 判断媒体是否匹配某个分类规则
func matchCategory(cat *model.CategoryRule, media *TmdbMedia) bool {
	// 检查 genre_ids
	if cat.GenreIds != "" {
		genreList := strings.Split(cat.GenreIds, ",")
		matched := false
		for _, g := range genreList {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			gid := 0
			fmt.Sscanf(g, "%d", &gid)
			for _, mg := range media.GenreIDs {
				if mg == gid {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 检查 original_language
	if cat.OriginalLanguage != "" {
		langList := strings.Split(cat.OriginalLanguage, ",")
		matched := false
		for _, l := range langList {
			l = strings.TrimSpace(l)
			if l != "" && media.OrigLanguage == l {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 检查自定义正则（命中即匹配，不需要其他条件）
	if cat.CustomRegex != "" {
		if re, err := regexp.Compile(cat.CustomRegex); err == nil {
			if re.MatchString(media.Title) || re.MatchString(media.OriginalTitle) {
				return true
			}
		}
		// 只有正则条件且未命中
		if cat.GenreIds == "" && cat.OriginalLanguage == "" && cat.OriginCountry == "" && cat.Ext == "" {
			return false
		}
	}

	// 检查 origin_country
	if cat.OriginCountry != "" {
		countryList := strings.Split(cat.OriginCountry, ",")
		matched := false
		for _, c := range countryList {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			for _, mc := range media.OrigCountry {
				if mc == c {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// sanitizeName 清洗名称中会破坏路径/被 115 拒绝的字符
func sanitizeName(name string) string {
	r := strings.NewReplacer("/", " ", "\\", " ", ":", "：", "*", " ", "?", "？", "\"", " ", "<", "(", ">", ")", "|", " ")
	return strings.TrimSpace(r.Replace(name))
}

// extractQualityInfo 从原始文件名提取画质信息（分辨率/来源/编码等）
// "Animal.Control.S04E01.1080p.NowPlayer.WEB-DL.AAC2.0.H.264-BlackTV.mkv"
//   → "1080p.WEB-DL.AAC2.0.H.264"
func extractQualityInfo(filename string) string {
	base := baseName(filename)
	parts := strings.Split(base, ".")

	var qualityParts []string
	// 跳过：标题段（前面的大写单词）和 S/E 编号段
	skipTitle := true
	for _, part := range parts {
		upper := strings.ToUpper(part)

		// 跳过 SxxExx / EPxx / 纯数字编号
		if (strings.HasPrefix(upper, "S") && strings.Contains(upper, "E")) ||
			(strings.HasPrefix(upper, "EP") && len(upper) <= 5) {
			skipTitle = false
			continue
		}
		if skipTitle {
			continue // 标题部分
		}

		// 收集画质相关字段
		if isQualityToken(part) {
			qualityParts = append(qualityParts, part)
		}
	}
	return strings.Join(qualityParts, ".")
}

// isQualityToken 判断是否为画质相关的 token
func isQualityToken(token string) bool {
	upper := strings.ToUpper(token)
	// 常见画质关键词
	qualityKeywords := []string{
		"1080P", "720P", "2160P", "4K", "480P",
		"WEB-DL", "WEB-DL", "BLURAY", "BLU-RAY", "REMUX", "HDTV", "WEBRIP", "DVDRIP",
		"H.264", "H.265", "X264", "X265", "HEVC", "AVC",
		"AAC", "AC3", "DTS", "FLAC", "TRUEHD", "DDP", "DD",
		"HDR", "DV", "SDR", "DOVI",
		"10BIT", "8BIT",
	}
	for _, kw := range qualityKeywords {
		if strings.Contains(upper, kw) {
			return true
		}
	}
	// AAC2.0, DTS-HD等复合token
	if strings.HasPrefix(upper, "AAC") || strings.HasPrefix(upper, "DTS") ||
		strings.HasPrefix(upper, "DDP") || strings.HasPrefix(upper, "TRUEHD") {
		return true
	}
	return false
}

func buildNewName(media *TmdbMedia, parsed *ParsedName, ext string) string {
	// 兼容旧调用（无 Handler 时的硬编码格式，模板引擎在 rename.go 中）
	media.Title = sanitizeName(media.Title)
	firstLetter := titleFirstLetter(media.Title)
	year := media.Year
	if year == "" {
		year = "0000"
	}
	folder := fmt.Sprintf("%s-%s-%s-[tmdb=%d]", firstLetter, media.Title, year, media.TmdbID)
	if media.MediaType == "movie" {
		file := fmt.Sprintf("%s (%s) [%d]%s", media.Title, year, media.TmdbID, ext)
		return folder + "/" + file
	}
	if parsed.Season > 0 {
		subFolder := fmt.Sprintf("Season %02d", parsed.Season)
		if parsed.Episode > 0 {
			file := fmt.Sprintf("%s - S%02dE%02d", media.Title, parsed.Season, parsed.Episode)
			if parsed.Quality != "" {
				file += "." + parsed.Quality
			}
			file += ext
			return folder + "/" + subFolder + "/" + file
		}
		return folder + "/" + subFolder
	}
	return folder
}

// buildNewNameWithTemplate 用模板引擎生成目标路径（Handler 方法，可读配置）
func buildNewNameWithTemplate(media *TmdbMedia, parsed *ParsedName, originalName string) string {
	media.Title = sanitizeName(media.Title)
	if renameTpl == nil {
		return buildNewName(media, parsed, pathExt(originalName)) // 降级到硬编码
	}
	ctx := buildRenameContext(media, parsed, originalName)
	var path string
	switch media.MediaType {
	case "movie":
		path = ctx.ApplyTemplate(renameTpl.MovieFolder) + "/" + ctx.ApplyTemplate(renameTpl.MovieFile)
	case "tv":
		path = ctx.ApplyTemplate(renameTpl.TVFolder) + "/" + ctx.ApplyTemplate(renameTpl.TVFile)
	default:
		path = ctx.ApplyTemplate(renameTpl.AVFolder) + "/" + ctx.ApplyTemplate(renameTpl.AVFile)
	}
	// 剧集需要插入 Season 目录（如果模板没有包含）
	if media.MediaType == "tv" && parsed.Season > 0 && !strings.Contains(path, "Season") {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			path = parts[0] + "/" + fmt.Sprintf("Season %02d", parsed.Season) + "/" + parts[1]
		}
	}
	return sanitizePath(path)
}

// titleFirstLetter 取标题首字母：英文取首字母，中文取拼音首字母（巴→B），数字为 #
func titleFirstLetter(title string) string {
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z':
			return strings.ToUpper(string(r))
		case r >= 'A' && r <= 'Z':
			return string(r)
		case r >= '0' && r <= '9':
			return "#"
		case r >= 0x4e00 && r <= 0x9fff: // 汉字
			py := pinyin.Pinyin(string(r), pinyin.NewArgs())
			if len(py) > 0 && len(py[0]) > 0 && py[0][0] != "" {
				c := py[0][0][0]
				if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
					return strings.ToUpper(string(c))
				}
			}
		}
	}
	return "0"
}

// checkByCloudSHA1 直接查网盘媒体库目标目录的 SHA1 去重（不依赖本地缓存表）
// 流程：根据 TMDB ID 计算目标目录路径 → 去网盘列出该目录的文件 → 比对 SHA1
// 返回 true=已存在 / false=不存在或目录为空
func checkByCloudSHA1(ops *pan115Ops, media *TmdbMedia, cfg *OrgConfig, libAbs string, currentSHA1 string) bool {
	if ops.cookie == "" || libAbs == "" || currentSHA1 == "" {
		return false // 条件不足，不判已存在（宁可重复入库也不误判）
	}

	// 计算目标目录（用与 buildNewName 相同的目录规则）
	firstLetter := titleFirstLetter(media.Title)
	year := media.Year
	if year == "" {
		year = "0000"
	}
	folderName := fmt.Sprintf("%s-%s-%s-[tmdb=%d]", firstLetter, media.Title, year, media.TmdbID)
	absDir := strings.TrimSuffix(libAbs, "/") + "/" + folderName

	// 查目标目录 cid
	cid, ok := cloudPathCid(ops.cookie, absDir)
	if !ok {
		return false // 目录不存在 → 新片
	}

	// 列出目录下的文件
	body, err := httpGet115UA("https://webapi.115.com/files",
		url.Values{
			"aid":      {"1"},
			"cid":      {cid},
			"show_dir": {"1"},
			"limit":    {"50"},
			"format":   {"json"},
		}, ops.cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return false // 查询失败，不判已存在
	}
	var r struct {
		State bool                      `json:"state"`
		Data  []map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return false
	}

	// 比对 SHA1：目录里有任何文件的 SHA1 与当前文件相同 → 已存在
	for _, d := range r.Data {
		if fmt.Sprint(d["f"]) != "1" {
			continue // 只看文件
		}
		sha1 := fmt.Sprint(d["sha"])
		if sha1 == "<nil>" {
			continue
		}
		if strings.EqualFold(sha1, currentSHA1) {
			return true
		}
	}
	return false // 目录存在但没有相同 SHA1 的文件 → 可能是不同版本/不同季
}

// cloudDirHasVideos 检查网盘目录下是否有视频文件（空目录或不存在都返回 false）
func cloudDirHasVideos(cookie, absPath string) bool {
	cid, ok := cloudPathCid(cookie, absPath)
	if !ok {
		return false
	}
	body, err := httpGet115UA("https://webapi.115.com/files",
		url.Values{
			"aid":      {"1"},
			"cid":      {cid},
			"show_dir": {"1"},
			"limit":    {"20"},
			"format":   {"json"},
		}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return true
	}
	var r struct {
		State bool                      `json:"state"`
		Data  []map[string]interface{} `json:"data"`
		Count int                       `json:"count"`
	}
	if json.Unmarshal(body, &r) != nil {
		return true
	}
	if !r.State || r.Count == 0 {
		return false
	}
	for _, d := range r.Data {
		if fmt.Sprint(d["f"]) == "1" {
			name := fmt.Sprint(d["n"])
			ext := strings.ToLower(pathExt(name))
			for _, ve := range []string{".mp4", ".mkv", ".ts", ".avi", ".mov", ".rmvb", ".webm", ".flv", ".m2ts", ".wmv", ".mpg", ".iso"} {
				if ext == ve {
					return true
				}
			}
		}
	}
	return false
}

// sha1ExistsInLibrary 文件 sha1 是否已存在于媒体库台账（同一文件不同命名也能识别）
func sha1ExistsInLibrary(sha1 string) bool {
	if sha1 == "" {
		return false
	}
	var count int64
	model.DB.Model(&model.SyncedFile{}).Where("sha1 = ? AND sha1 != ''", sha1).Count(&count)
	return count > 0
}

// checkExistsVerified 去重判定：本地记录命中后到网盘验证目标路径仍存在，
// 记录失效（库被清空/内容被移走）则自动删除记录并视为不存在——
// 空库误判"已存在"的根治方案；网盘查询失败时保守按存在处理
func checkExistsVerified(ops *pan115Ops, media *TmdbMedia, cfg *OrgConfig, libAbs string) bool {
	var rec model.MediaLibrary
	if err := model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).First(&rec).Error; err != nil {
		return false
	}
	// 网盘验证：检查记录的目标目录下是否还有视频文件（空目录 = 已清空 = 记录过期）
	if ops.cookie != "" && libAbs != "" && rec.TargetPath != "" {
		dirPart := path.Dir(rec.TargetPath)
		absDir := path.Join(libAbs, dirPart)
		if !cloudDirHasVideos(ops.cookie, absDir) {
			log.Printf("[整理] ✦ 去重记录已过期（网盘目录为空或不存在），自动清除: %s (%s) tmdb=%d 旧目标=%s",
				rec.Title, rec.Year, rec.TmdbID, rec.TargetPath)
			model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).Delete(&model.MediaLibrary{})
			return false
		}
	}
	log.Printf("[整理] ○ 已存在: %s (%s) tmdb=%d 记录于 %s 目标=%s",
		rec.Title, rec.Year, rec.TmdbID, rec.CreatedAt.Format("2006-01-02 15:04"), rec.TargetPath)
	return true
}

// cloudPathExistsCk 校验网盘绝对路径是否存在（webapi files/getid）
func cloudPathExistsCk(cookie, absPath string) bool {
	body, err := httpGet115UA("https://webapi.115.com/files/getid",
		url.Values{"path": {absPath}}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return true // 查询失败保守按存在
	}
	var r struct {
		State bool                      `json:"state"`
		Data  []map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return true
	}
	return r.State && len(r.Data) > 0
}

// recordMedia 记录已整理的媒体到数据库
func recordMedia(media *TmdbMedia, category, targetPath string) {
	record := &model.MediaLibrary{
		TmdbID:       media.TmdbID,
		Title:        media.Title,
		OriginalTitle: media.OriginalTitle,
		Year:         media.Year,
		MediaType:    media.MediaType,
		Category:     category,
		TargetPath:   targetPath,
		OrigLanguage: media.OrigLanguage,
		OrigCountry:  strings.Join(media.OrigCountry, ","),
	}
	model.DB.Save(record)
}

// ==================== 文件分类（附属文件保留规则） ====================

// videoExts 支持的视频后缀
var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".ts": true, ".avi": true, ".mov": true,
	".rmvb": true, ".webm": true, ".flv": true, ".m2ts": true,
	".wmv": true, ".mpg": true, ".iso": true, ".m4v": true,
}

// subtitleExts 支持的字幕后缀
var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true,
	".idx": true, ".sup": true, ".vtt": true,
}

// standardImageNames Emby/Kodi 标准命名的图片（不含后缀，小写匹配）
var standardImageNames = map[string]bool{
	"poster": true, "fanart": true, "backdrop": true, "banner": true,
	"thumb": true, "landscape": true, "logo": true, "logo-clear": true,
	"clearart": true, "clearlogo": true, "disc": true, "discart": true,
}

// FileType 文件分类类型
type FileType int

const (
	FileTypeVideo    FileType = iota // 视频文件
	FileTypeSubtitle                 // 字幕文件
	FileTypeNFO                      // NFO 媒体信息
	FileTypeStdImage                 // 标准命名图片
	FileTypeJunk                     // 无用文件（广告等）
)

// classifyFile 判断文件类型
func classifyFile(name string) FileType {
	ext := strings.ToLower(pathExt(name))
	base := strings.ToLower(baseName(name))

	// 视频
	if videoExts[ext] {
		return FileTypeVideo
	}
	// 字幕
	if subtitleExts[ext] {
		return FileTypeSubtitle
	}
	// NFO
	if ext == ".nfo" || ext == ".xml" {
		return FileTypeNFO
	}
	// 图片：只保留标准命名的
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
		if standardImageNames[base] {
			return FileTypeStdImage
		}
		return FileTypeJunk
	}
	// 其他文件（txt 等广告文件）
	return FileTypeJunk
}

// pathExt 取文件后缀（含点）
func pathExt(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx:]
	}
	return ""
}

// baseName 取文件名（不含后缀）
func baseName(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[:idx]
	}
	return name
}

// ==================== 目录级遍历（获取待整理目录结构） ====================

// dirEntry 待整理目录中的一个条目（文件或子目录）
type dirEntry struct {
	Fid   string // 文件 id
	Name  string // 文件名
	IsDir bool   // 是否文件夹
	Size  int64  // 文件大小
	Cid   string // 子目录 cid（仅文件夹有）
	Sha1  string // 文件 sha1（散文件去重用）
}

// listPendingTopLevel 列出待整理目录下的顶层条目（不递归）
func listPendingTopLevel(ops *pan115Ops, cid string) ([]dirEntry, error) {
	var entries []dirEntry
	offset := 0
	for {
		raw, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		for _, d := range raw {
			isDir := fmt.Sprint(d["f"]) == "0"
			fid := fmt.Sprint(d["fid"])
			if isDir && (fid == "" || fid == "<nil>") {
				// webapi 列表中目录自身 id 在 cid 字段（无 fid），移动目录必须用它
				fid = fmt.Sprint(d["cid"])
			}
			e := dirEntry{
				Fid:  fid,
				Name: fmt.Sprint(d["n"]),
				IsDir: isDir,
				Cid:  fmt.Sprint(d["cid"]),
			}
			sha1 := fmt.Sprint(d["sha"])
			if sha1 != "<nil>" {
				e.Sha1 = sha1
			}
			if s, ok := d["s"].(float64); ok {
				e.Size = int64(s)
			}
			entries = append(entries, e)
		}
		if len(raw) == 0 || offset+len(raw) >= count {
			break
		}
		offset += len(raw)
	}
	return entries, nil
}

// collectDirFiles 递归收集某个 cid 目录下的所有文件（包括子目录），返回带 fid 的列表
func collectDirFiles(ops *pan115Ops, cid, basePath string) ([]remoteFile, error) {
	var files []remoteFile
	offset := 0
	for {
		raw, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		for _, d := range raw {
			isDir := fmt.Sprint(d["f"]) == "0"
			name := fmt.Sprint(d["n"])
			if isDir {
				subFiles, err := collectDirFiles(ops, fmt.Sprint(d["cid"]), basePath+"/"+name)
				if err != nil {
					return nil, err
				}
				files = append(files, subFiles...)
			} else {
				size := int64(0)
				if s, ok := d["s"].(float64); ok {
					size = int64(s)
				}
				sha1 := fmt.Sprint(d["sha"])
				if sha1 == "<nil>" {
					sha1 = ""
				}
				files = append(files, remoteFile{
					Fid:  fmt.Sprint(d["fid"]),
					Name: name,
					Path: basePath,
					Size: size,
					Sha1: sha1,
				})
			}
		}
		if len(raw) == 0 || offset+len(raw) >= count {
			break
		}
		offset += len(raw)
	}
	return files, nil
}

// orgGuards 整理防误伤守卫：
// 当扫描根（待整理/转存目录）位于媒体库、已存在、冗余三棵子树的同级或上层时，
// 这些子树内的条目一律跳过——绝不把库内内容当待整理素材重排或搬进冗余。
// 正常布局（待整理在库内/库外独立）不受影响；OpenAPI 无 Cookie 通道取不到
// 目录绝对路径时守卫自动失效（靠 trigger 层校验兜底）
type orgGuards struct {
	cookie    string
	memo      map[string]dirInfo
	absCache  map[string]string
	protected []string // 受保护子树绝对路径（尾 / 已去除）
	active    bool     // 扫描根是否覆盖到任一保护子树
}

// newOrgGuards 计算扫描根与三棵保护子树的空间关系
func newOrgGuards(cookie, scanCid string, cfg *OrgConfig) *orgGuards {
	g := &orgGuards{cookie: cookie, memo: map[string]dirInfo{}, absCache: map[string]string{}}
	if cookie == "" {
		return g
	}
	for _, cid := range []string{cfg.Library, cfg.Existing, cfg.Redundant} {
		if cid == "" {
			continue
		}
		if a := g.absOf(cid); a != "" {
			g.protected = append(g.protected, strings.TrimSuffix(a, "/"))
		}
	}
	scanAbs := strings.TrimSuffix(g.absOf(scanCid), "/")
	for _, p := range g.protected {
		if scanAbs != "" && (p == scanAbs || strings.HasPrefix(p, scanAbs+"/")) {
			g.active = true
			break
		}
	}
	return g
}

func (g *orgGuards) absOf(cid string) string {
	if a, ok := g.absCache[cid]; ok {
		return a
	}
	a := absPathOf(g.cookie, cid, g.memo)
	g.absCache[cid] = a
	return a
}

// skip 报告条目是否位于保护子树内（目录传自身 cid，文件传父目录 cid）。
// 仅在 active 模式下生效；路径取不到时宁可放行（由日志暴露异常布局）
func (g *orgGuards) skip(cid string) bool {
	if g == nil || !g.active || cid == "" {
		return false
	}
	a := strings.TrimSuffix(g.absOf(cid), "/")
	if a == "" {
		return false
	}
	for _, p := range g.protected {
		if a == p || strings.HasPrefix(a+"/", p+"/") {
			return true
		}
	}
	return false
}

// runOrganizeEngine 整理引擎核心逻辑
// 按目录级别整理：识别视频→分类→移动整个目录（视频+字幕+NFO+标准图片）到影视库
func runOrganizeEngine(ops *pan115Ops, cfg *OrgConfig, onLog func(string)) ([]OrganizeResult, int) {
	results := []OrganizeResult{}
	successCount := 0

	// 加载 TMDB 客户端
	tc, err := loadTmdbClient(nil)
	if err != nil {
		onLog("✗ TMDB 配置错误: " + err.Error())
		return results, 0
	}

	// 加载替换规则
	replaceRules := loadReplaceRules()

	// 加载重命名模板配置
	if v := modelSettingValue("org-rename"); v != "" {
		var saved RenameConfig
		if json.Unmarshal([]byte(v), &saved) == nil {
			renameTpl = &saved
		}
	} else {
		renameTpl = &RenameConfig{
			MovieFolder: "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
			MovieFile: "{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}",
			TVFolder: "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
			TVFile: "{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}",
			AVFolder: "{first_letter}-{title}",
			AVFile: "{title}<.{resource_pix}><.{resource_type}>{ext}",
		}
	}

	// 库根绝对路径（去重记录的网盘验证用；OpenAPI 通道取不到则跳过验证）
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library, map[string]dirInfo{})
	}

	// 获取待整理目录下的顶层条目
	topEntries, err := listPendingTopLevel(ops, cfg.Pending)
	if err != nil {
		onLog("✗ 遍历待整理目录失败: " + err.Error())
		return results, 0
	}

	if len(topEntries) == 0 {
		onLog("○ 待整理目录为空")
		return results, 0
	}

	// 四个工作区根目录（媒体库/待整理/已存在/冗余）永不被移动：
	// 引擎只往它们里面放内容，目录自身绝不能被当作影视条目处理
	// （否则会出现"已经存在文件夹移进自己"，或媒体库嵌在待整理内时整库被搬进已存在）
	excluded := map[string]bool{cfg.Library: true, cfg.Existing: true, cfg.Redundant: true, cfg.Pending: true}
	filtered := topEntries[:0]
	for _, e := range topEntries {
		if e.IsDir && excluded[e.Cid] {
			onLog(fmt.Sprintf("○ 跳过整理工作区目录: %s/", e.Name))
			continue
		}
		filtered = append(filtered, e)
	}
	topEntries = filtered
	if len(topEntries) == 0 {
		onLog("○ 待整理目录为空（仅剩整理工作区目录）")
		return results, 0
	}

	onLog(fmt.Sprintf("发现 %d 个条目，开始整理...", len(topEntries)))

	guards := newOrgGuards(ops.cookie, cfg.Pending, cfg)
	if guards.active {
		onLog("⚠ 扫描根覆盖到媒体库/已存在/冗余目录，这些子树内的条目将被跳过（防误整理库内容）")
	}

	for _, entry := range topEntries {
		results = append(results, processEntry(ops, cfg, tc, replaceRules, guards, entry, libAbs, onLog, 0, &successCount)...)
		time.Sleep(300 * time.Millisecond)
	}

	return results, successCount
}

// processEntry 处理一个顶层条目：
//   - 目录不含直接视频文件但含子目录 → 容器目录（如用户把多部剧放进同一文件夹），
//     递归处理每个子目录（每部剧独立识别入库），容器自身最后移到冗余
//   - 其余目录 → 单部影视目录
//   - 文件 → 散视频
func processEntry(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, guards *orgGuards, entry dirEntry, libAbs string, onLog func(string), depth int, successCount *int) []OrganizeResult {
	results := []OrganizeResult{}
	// 库子树防护：目录传自身 cid，文件传父目录 cid；命中保护子树直接放行不处理
	if guards.skip(entry.Cid) {
		onLog(fmt.Sprintf("○ 跳过媒体库/工作区内条目: %s（整理不处理库内内容）", entry.Name))
		return results
	}
	if !entry.IsDir {
		if classifyFile(entry.Name) != FileTypeVideo {
			return results
		}
		if cfg.MinSize > 0 && entry.Size > 0 && entry.Size < cfg.MinSize*1024*1024 {
			return results
		}
		f := remoteFile{Fid: entry.Fid, Name: entry.Name, Size: entry.Size, Sha1: entry.Sha1}
		result := processSingleFileWithSiblings(ops, cfg, tc, replaceRules, f, libAbs, onLog)
		results = append(results, result...)
		for _, r := range results {
			if r.Status == "success" {
				*successCount++
			}
		}
		return results
	}

	// 列直接子条目判断是否为容器目录
	direct, err := listPendingTopLevel(ops, entry.Cid)
	if err == nil && depth < 3 {
		excluded := map[string]bool{cfg.Library: true, cfg.Existing: true, cfg.Redundant: true, cfg.Pending: true}
		hasDirectVideo := false
		var subDirs []dirEntry
		for _, c := range direct {
			if c.IsDir {
				if !excluded[c.Cid] {
					subDirs = append(subDirs, c)
				}
			} else if classifyFile(c.Name) == FileTypeVideo {
				hasDirectVideo = true
			}
		}
		if !hasDirectVideo && len(subDirs) > 0 {
			onLog(fmt.Sprintf("▣ %s/ 为容器目录（无直接视频，含 %d 个子目录），逐个处理", entry.Name, len(subDirs)))
			for _, child := range subDirs {
				results = append(results, processEntry(ops, cfg, tc, replaceRules, guards, child, libAbs, onLog, depth+1, successCount)...)
			}
			// 容器壳处理：重新列目录确认真的空了才移冗余；
			// 有残留（移动失败/未识别跳过的条目）时保留原地，避免误吞内容目录
			remaining, relistErr := listPendingTopLevel(ops, entry.Cid)
			if relistErr != nil {
				onLog(fmt.Sprintf("○ %s/ - 复查目录失败，保留原地: %v", entry.Name, relistErr))
				return results
			}
			if len(remaining) > 0 {
				// 散落的纯垃圾文件（txt/url 广告等）可随壳一起清进冗余
				allJunk, junkFids := true, []string{}
				for _, r := range remaining {
					if r.IsDir || classifyFile(r.Name) != FileTypeJunk {
						allJunk = false
						break
					}
					junkFids = append(junkFids, r.Fid)
				}
				if allJunk && len(junkFids) > 0 {
					if err := ops.moveFiles(cfg.Redundant, junkFids); err == nil {
						onLog(fmt.Sprintf("○ %s/ - 容器内 %d 个垃圾文件已移到冗余", entry.Name, len(junkFids)))
						remaining, _ = listPendingTopLevel(ops, entry.Cid)
					}
				}
			}
			if len(remaining) == 0 {
				if err := ops.moveFiles(cfg.Redundant, []string{entry.Fid}); err != nil {
					onLog(fmt.Sprintf("○ %s/ - 空容器目录移到冗余失败: %v", entry.Name, err))
				} else {
					onLog(fmt.Sprintf("○ %s/ - 空容器目录已移到冗余", entry.Name))
				}
			} else {
				onLog(fmt.Sprintf("○ %s/ - 容器目录仍有 %d 个残留条目，保留原地", entry.Name, len(remaining)))
			}
			return results
		}
	}

	// 单部影视目录：收集所有文件识别入库
	subFiles, err := collectDirFiles(ops, entry.Cid, entry.Name)
	if err != nil {
		onLog(fmt.Sprintf("✗ %s/ - 遍历失败: %v", entry.Name, err))
		return results
	}
	if len(subFiles) == 0 {
		return results
	}
	result := processDir(ops, cfg, tc, replaceRules, entry, subFiles, libAbs, onLog)
	results = append(results, result...)
	for _, r := range result {
		if r.Status == "success" {
			*successCount++
		}
	}
	return results
}

// processDir 处理一个子目录（包含多个文件的影视目录）
func processDir(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, dir dirEntry, files []remoteFile, libAbs string, onLog func(string)) []OrganizeResult {
	var results []OrganizeResult

	// 找出视频文件
	var videoFiles []remoteFile
	for _, f := range files {
		if classifyFile(f.Name) == FileTypeVideo {
			videoFiles = append(videoFiles, f)
		}
	}

	if len(videoFiles) == 0 {
		// 没有视频文件，整个目录移到冗余
		allFids := make([]string, 0, len(files))
		for _, f := range files {
			allFids = append(allFids, f.Fid)
		}
		if err := ops.moveFiles(cfg.Redundant, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到冗余失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ - 无视频文件，已移到冗余", dir.Name))
		}
		return results
	}

	// 识别第一个视频（取最大的文件作为主视频）
	mainVideo := videoFiles[0]
	for _, v := range videoFiles {
		if v.Size > mainVideo.Size {
			mainVideo = v
		}
	}

	// 应用替换规则
	name := mainVideo.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
	onLog(fmt.Sprintf("▶ 开始识别: %s/（样本: %s）", dir.Name, mainVideo.Name))

	// AV 番号检测：文件名或目录名含番号格式（如 START-622、MIDV-001）时
	// 跳过 TMDB 识别，直接用番号作为标题归入 AV 分类
	if avNum := detectAVNumber(dir.Name, mainVideo.Name); avNum != "" {
		onLog(fmt.Sprintf("✦ 检测到 AV 番号: %s（跳过 TMDB）", avNum))
		media := &TmdbMedia{
			Title:     avNum,
			MediaType: "av",
		}
		return processAVDirectory(ops, cfg, media, dir, files, onLog, results)
	}

	parsed := parseFileName(name)

	// 文件名无法提取标题 → 用目录名识别（目录名通常比文件名规范）
	// 场景：/西游记.1987/ep01.mkv — 文件名只有集数，目录名有标题和年份
	useDirName := false
	if parsed.Title == "" || isEpisodeOnly(parsed.Title) {
		dirParsed := parseFileName(dir.Name)
		if dirParsed.Title != "" && !isEpisodeOnly(dirParsed.Title) {
			onLog(fmt.Sprintf("▣ 文件名 %q 无法识别，改用目录名 %q", name, dir.Name))
			// 用目录名做识别，但保留文件名解析出的季集号
			if parsed.Season == 0 {
				parsed.Season = dirParsed.Season
			}
			if parsed.Episode == 0 {
				parsed.Episode = dirParsed.Episode
			}
			if parsed.Year == "" {
				parsed.Year = dirParsed.Year
			}
			parsed.IsTV = dirParsed.IsTV || parsed.IsTV
			parsed = dirParsed // 用目录名的标题/年份
			// 恢复文件名中的季集号（如果目录名没有的话）
			if parsed.Season == 0 && parseFileName(name).Season > 0 {
				parsed.Season = parseFileName(name).Season
			}
			if parsed.Episode == 0 && parseFileName(name).Episode > 0 {
				parsed.Episode = parseFileName(name).Episode
			}
			useDirName = true
		}
	}

	if parsed.Title == "" {
		// 文件名和目录名都无法识别，移到冗余
		moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
		onLog(fmt.Sprintf("○ %s/ - 无法提取标题，已移到冗余", dir.Name))
		return results
	}

	// TMDB 识别
	media, err := tc.recognize(parsed)
	if err != nil || media == nil {
		// 文件名识别失败 → 如果还没试过目录名，用目录名再识别一次
		if !useDirName {
			dirParsed := parseFileName(dir.Name)
			if dirParsed.Title != "" {
				onLog(fmt.Sprintf("▣ 文件名识别失败，改用目录名 %q 重试", dir.Name))
				// 保留文件名的季集号
				if dirParsed.Season == 0 {
					dirParsed.Season = parsed.Season
				}
				if dirParsed.Episode == 0 {
					dirParsed.Episode = parsed.Episode
				}
				media, err = tc.recognize(dirParsed)
			}
		}
		if err != nil || media == nil {
			msg := "TMDB 未找到匹配"
			if err != nil {
				msg = "TMDB 识别失败: " + err.Error()
			}
			moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
			onLog(fmt.Sprintf("○ %s/ - %s，已移到冗余", dir.Name, msg))
			return results
		}
	}

	onLog(fmt.Sprintf("✦ 识别成功: %s/ → %s (%s) [%s/tmdb=%d]", dir.Name, media.Title, media.Year, media.MediaType, media.TmdbID))

	// 直接查网盘去重（不依赖本地缓存表，不会过期）
	// 检查网盘目标目录里是否有相同 SHA1 的文件
	if checkByCloudSHA1(ops, media, cfg, libAbs, mainVideo.Sha1) {
		if err := ops.moveFiles(cfg.Existing, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到已存在失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ → 已存在: %s (%s)，已移到已存在目录", dir.Name, media.Title, media.Year))
		}
		for _, vf := range videoFiles {
			results = append(results, OrganizeResult{FileName: vf.Name, Status: "exists", Title: media.Title, Year: media.Year, MediaType: media.MediaType,
				Message: "网盘已有相同文件"})
		}
		return results
	}

	// 洗版判定：本地记录命中且新版更优时替换（保留洗版逻辑，用本地记录）
	if rec, ok := lookupMediaRecord(media); ok {
		if tryWashReplace(ops, cfg, media, mainVideo.Name, rec.TargetPath, onLog) {
			// 旧版已让位，落入下方正常入库
			_ = rec
		}
	}


	// 不存在 → 分类 + 移动到我的影视库
	category := classifyMedia(media)
	newPath := buildNewNameWithTemplate(media, parsed, mainVideo.Name)
	targetDir := mediaTypeCategory(media.MediaType) + "/" + category + "/" + pathDir(newPath)

	_ = targetDir // 目标目录在下方按新结构创建（根目录 + 季目录）

	// 按文件分类移动（规范结构）：
	//   视频 + 字幕 → 季目录（电影为根目录）；NFO + 标准封面图 → 剧集根目录；垃圾 → 冗余
	parts := strings.Split(newPath, "/")
	rootRel := mediaTypeCategory(media.MediaType) + "/" + category + "/" + parts[0]
	onLog(fmt.Sprintf("▣ 目标目录就绪: %s", rootRel))
	rootCid, err := ops.ensurePath(cfg.Library, rootRel)
	if err != nil {
		onLog(fmt.Sprintf("✗ %s/ - 创建目录失败: %v（目标=%q）", dir.Name, err, rootRel))
		results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "创建目录失败: " + err.Error()})
		return results
	}
	mediaCid := rootCid
	if media.MediaType == "tv" && len(parts) >= 2 { // Season XX 层
		onLog(fmt.Sprintf("▣ 季目录就绪: %s/%s", rootRel, parts[1]))
		mediaCid, err = ops.ensurePath(cfg.Library, rootRel+"/"+parts[1])
		if err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 创建季目录失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "创建季目录失败: " + err.Error()})
			return results
		}
	}

	var mediaFids, metaFids, junkFids []string
	for _, f := range files {
		switch classifyFile(f.Name) {
		case FileTypeVideo, FileTypeSubtitle:
			mediaFids = append(mediaFids, f.Fid)
		case FileTypeNFO, FileTypeStdImage:
			metaFids = append(metaFids, f.Fid) // 封面/NFO 放剧集根目录
		default:
			junkFids = append(junkFids, f.Fid)
		}
	}

	// 先在源目录重命名（批量 batch_rename），再移动到目标
	renameBeforeMove(ops, media, videoFiles, files, onLog)

	// 视频/字幕 → 季目录（电影为根目录）
	if len(mediaFids) > 0 {
		if err := ops.moveFiles(mediaCid, mediaFids); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动文件失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "移动文件失败: " + err.Error()})
			return results
		}
	}
	// 封面/NFO → 剧集根目录
	if len(metaFids) > 0 && mediaCid != rootCid {
		if err := ops.moveFiles(rootCid, metaFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 封面/NFO 移动到根目录失败（留在季目录）: %v", dir.Name, err))
		}
	} else if len(metaFids) > 0 {
		// 电影：全部进根目录
		if err := ops.moveFiles(rootCid, metaFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 封面/NFO 移动失败: %v", dir.Name, err))
		}
	}

	// 移动无用文件到冗余
	if len(junkFids) > 0 {
		junkCid, err := ops.ensurePath(cfg.Redundant, dir.Name)
		if err == nil {
			ops.moveFiles(junkCid, junkFids)
		}
	}

	// 整理完毕：已移空的源目录移到冗余（避免待整理目录残留空壳）
	if err := ops.moveFiles(cfg.Redundant, []string{dir.Fid}); err != nil {
		onLog(fmt.Sprintf("○ %s/ - 空源目录移到冗余失败: %v", dir.Name, err))
	} else {
		onLog(fmt.Sprintf("○ %s/ - 空源目录已移到冗余", dir.Name))
	}

	// 记录到数据库
	recordMedia(media, category, targetDir+"/"+pathBase(newPath))

	// 生成结果
	for _, vf := range videoFiles {
		results = append(results, OrganizeResult{
			FileName:  vf.Name,
			Status:    "success",
			TmdbID:    media.TmdbID,
			Title:     media.Title,
			Year:      media.Year,
			MediaType: media.MediaType,
			Category:  category,
			TargetDir: targetDir,
			Message:   fmt.Sprintf("→ %s (%s) [%s]", media.Title, media.Year, media.MediaType),
		})
	}
	onLog(fmt.Sprintf("✓ %s/ → %s (%s) [%s/%s] → %s", dir.Name, media.Title, media.Year, category, media.MediaType, targetDir))

	return results
}

// processSingleFileWithSiblings 散文件批量处理：识别第一个文件后，
// 同前缀的其他散文件共享识别结果（一部剧 24 集只需 1 次 TMDB 调用）
// 前缀判定：文件名去掉 EP/SxxExx/集数 部分后剩余部分相同
func processSingleFileWithSiblings(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, libAbs string, onLog func(string)) []OrganizeResult {
	// 先识别主文件
	result := processSingleFile(ops, cfg, tc, replaceRules, f, libAbs, onLog)
	if result.Status != "success" {
		return []OrganizeResult{result}
	}

	// 识别成功 → 列出待整理目录中的其他散文件，找同前缀的视频
	mainPrefix := extractSeriesPrefix(f.Name)
	if mainPrefix == "" {
		return []OrganizeResult{result} // 无法提取前缀，不批量处理
	}

	entries, _, err := ops.listEntries(cfg.Pending, 0)
	if err != nil {
		return []OrganizeResult{result} // 列表失败，只处理主文件
	}

	var siblings []remoteFile
	for _, e := range entries {
		if fmt.Sprint(e["f"]) != "1" { // 只看文件
			continue
		}
		name := fmt.Sprint(e["n"])
		if name == f.Name {
			continue // 跳过主文件（已处理）
		}
		if classifyFile(name) != FileTypeVideo {
			continue
		}
		if extractSeriesPrefix(name) == mainPrefix {
			siblings = append(siblings, remoteFile{
				Fid:  fmt.Sprint(e["fid"]),
				Name: name,
				Size: 0,
				Sha1: fmt.Sprint(e["sha"]),
			})
		}
	}

	if len(siblings) == 0 {
		return []OrganizeResult{result} // 没有同前缀的其他文件
	}

	onLog(fmt.Sprintf("▣ 发现 %d 个同前缀散文件，共享识别结果批量处理", len(siblings)))

	// 构建与主文件相同的目标（分类/目录/媒体信息从 result 提取不行，
	// 需要重新构造——用主文件的 parsed 和 media）
	// 简化：直接用 processDir 逻辑处理剩余文件
	var allResults = []OrganizeResult{result}
	successCount := 1

	for _, sib := range siblings {
		sibResult := organizeIdentifiedFile(ops, cfg, tc, replaceRules, sib, libAbs, result, onLog)
		allResults = append(allResults, sibResult)
		if sibResult.Status == "success" {
			successCount++
		}
	}

	onLog(fmt.Sprintf("✓ 散文件批量完成: 共 %d 个文件（成功 %d）", len(allResults), successCount))
	return allResults
}

// extractSeriesPrefix 提取剧集文件名的系列前缀（去掉 EP/SxxExx/集数部分）
// "BLJXD.2026.EP01.HD1080P..." → "BLJXD.2026"
// "Show.Name.S01E05.720p..." → "Show.Name"
func extractSeriesPrefix(name string) string {
	base := baseName(name)
	// 按 . 分割，找 EP/SxxExx 位置截断
	parts := strings.Split(base, ".")
	for i, part := range parts {
		upper := strings.ToUpper(part)
		// 匹配 EP01, E01, S01E05, S01E05E06 等
		if len(upper) >= 3 {
			if strings.HasPrefix(upper, "EP") && len(upper) <= 5 && isAllDigits(upper[2:]) {
				return strings.Join(parts[:i], ".")
			}
			if strings.HasPrefix(upper, "S") && strings.Contains(upper, "E") && len(upper) <= 10 {
				return strings.Join(parts[:i], ".")
			}
		}
		// 匹配纯数字段（可能是集数）
		if isAllDigits(part) && len(part) <= 3 && i > 0 {
			// 前一段不是年份（4位）则认为这是集数
			if !isAllDigits(parts[i-1]) || len(parts[i-1]) != 4 {
				return strings.Join(parts[:i], ".")
			}
		}
	}
	return base // 没找到 EP 标记，返回整个基名
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// organizeIdentifiedFile 用已识别的媒体信息处理单个散文件（跳过 TMDB 识别）
func organizeIdentifiedFile(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, libAbs string, mainResult OrganizeResult, onLog func(string)) OrganizeResult {
	result := OrganizeResult{FileName: f.Name}

	// sha1 去重
	if sha1ExistsInLibrary(f.Sha1) {
		ops.moveFiles(cfg.Existing, []string{f.Fid})
		onLog(fmt.Sprintf("○ %s - sha1已存在，已移到已存在目录", f.Name))
		return OrganizeResult{FileName: f.Name, Status: "exists", Message: "sha1已存在"}
	}

	// 解析文件名获取季集号
	name := f.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
	parsed := parseFileName(name)

	// 从主结果提取媒体信息
	// 重新调 recognize 太浪费——用 media 重建
	// 简化：直接构造目标路径
	media := &TmdbMedia{
		Title:     mainResult.Title,
		Year:      mainResult.Year,
		MediaType: mainResult.MediaType,
		TmdbID:    mainResult.TmdbID,
	}

	category := mainResult.Category
	newPath := buildNewNameWithTemplate(media, parsed, f.Name)
	targetDir := mediaTypeCategory(media.MediaType) + "/" + category + "/" + pathDir(newPath)

	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		result.Status = "failed"
		result.Message = "创建目录失败: " + err.Error()
		return result
	}

	if err := ops.moveFiles(targetCid, []string{f.Fid}); err != nil {
		result.Status = "failed"
		result.Message = "移动失败: " + err.Error()
		return result
	}

	// 重命名为标准名
	if stdName := pathBase(newPath); stdName != "" && stdName != f.Name {
		if err := ops.rename(f.Fid, stdName); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", f.Name, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s", f.Name, stdName))
		}
	}

	result.Category = category
	result.TargetDir = targetDir
	result.TmdbID = mainResult.TmdbID
	result.Title = mainResult.Title
	result.Year = mainResult.Year
	result.MediaType = mainResult.MediaType
	result.Status = "success"
	result.Message = fmt.Sprintf("→ %s (%s) [%s] → %s", mainResult.Title, mainResult.Year, category, targetDir)
	onLog(fmt.Sprintf("✓ %s → %s", f.Name, stdPath(newPath)))
	return result
}

func stdPath(p string) string {
	return p
}

// processSingleFile 处理待整理目录下的顶层单独视频文件
func processSingleFile(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, libAbs string, onLog func(string)) OrganizeResult {
	result := OrganizeResult{FileName: f.Name}

	// 应用替换规则
	name := f.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
	// SHA1 去重移到 TMDB 识别后（需要 media 信息来计算目标目录）
	onLog(fmt.Sprintf("▶ 开始识别: %s", f.Name))
	parsed := parseFileName(name)
	oldBase := baseName(f.Name)
	if parsed.Title == "" {
		// 无法识别，移到冗余（附件随行，避免字幕变孤儿）
		moveQuietly(ops, cfg.Redundant, []string{f.Fid}, f.Name, onLog)
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Redundant, false, onLog)
		result.Status = "failed"
		result.Message = "无法提取标题，已移到冗余"
		onLog(fmt.Sprintf("✗ %s - 无法提取标题，已移到冗余", f.Name))
		return result
	}

	// TMDB 识别
	media, err := tc.recognize(parsed)
	if err != nil || media == nil {
		moveQuietly(ops, cfg.Redundant, []string{f.Fid}, f.Name, onLog)
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Redundant, false, onLog)
		result.Status = "failed"
		result.Message = "TMDB 识别失败，已移到冗余"
		onLog(fmt.Sprintf("✗ %s - TMDB 识别失败，已移到冗余", f.Name))
		return result
	}

	result.TmdbID = media.TmdbID
	result.Title = media.Title
	result.Year = media.Year
	result.MediaType = media.MediaType

	onLog(fmt.Sprintf("✦ 识别成功: %s → %s (%s) [%s/tmdb=%d]", f.Name, media.Title, media.Year, media.MediaType, media.TmdbID))

	// 直接查网盘去重（不依赖本地缓存表）
	if checkByCloudSHA1(ops, media, cfg, libAbs, f.Sha1) {
		ops.moveFiles(cfg.Existing, []string{f.Fid})
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Existing, false, onLog)
		result.Status = "exists"
		result.Message = fmt.Sprintf("已存在: %s (%s)，已移到已存在目录", media.Title, media.Year)
		onLog(fmt.Sprintf("○ %s → 已存在: %s (%s)", f.Name, media.Title, media.Year))
		return result
	}

	// 分类 + 移动到影视库
	category := classifyMedia(media)
	newPath := buildNewNameWithTemplate(media, parsed, f.Name)
	targetDir := mediaTypeCategory(media.MediaType) + "/" + category + "/" + pathDir(newPath)

	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		result.Status = "failed"
		result.Message = "创建目录失败: " + err.Error()
		onLog(fmt.Sprintf("✗ %s - 创建目录失败: %v", f.Name, err))
		return result
	}

	if err := ops.moveFiles(targetCid, []string{f.Fid}); err != nil {
		result.Status = "failed"
		result.Message = "移动文件失败: " + err.Error()
		onLog(fmt.Sprintf("✗ %s - 移动失败: %v", f.Name, err))
		return result
	}
	// 视频本体重命名为标准名
	if stdName := pathBase(newPath); stdName != "" && stdName != f.Name {
		if err := ops.rename(f.Fid, stdName); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", f.Name, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s", f.Name, stdName))
		}
	}

	onLog(fmt.Sprintf("▣ 目标目录就绪: %s", category+"/"+strings.Split(newPath, "/")[0]))
	// 成功入库：附件随行并按视频新名重命名字幕（播放器按视频名匹配外挂字幕）
	newBase := baseName(pathBase(newPath))
	moveSiblingAttachments(ops, cfg.Pending, oldBase, newBase, targetCid, true, onLog)

	recordMedia(media, category, targetDir+"/"+pathBase(newPath))
	result.Category = category
	result.TargetDir = targetDir
	result.Status = "success"
	result.Message = fmt.Sprintf("→ %s (%s) [%s/%s] → %s", media.Title, media.Year, category, media.MediaType, targetDir)
	onLog(fmt.Sprintf("✓ %s → %s (%s) [%s/%s] → %s", f.Name, media.Title, media.Year, category, media.MediaType, targetDir))

	return result
}

// pathDir 取路径中的目录部分（最后一个 / 之前）
func pathDir(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[:idx]
	}
	return ""
}

// pathBase 取路径中的文件名部分（最后一个 / 之后）
func pathBase(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// isEpisodeOnly 判断标题是否只是集数/编号（如 "ep01"、"E05"、"01"）
func isEpisodeOnly(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return true
	}
	// ep01, e01, 01, 1
	cleaned := strings.TrimPrefix(strings.TrimPrefix(t, "ep"), "e")
	return isAllDigits(cleaned) && len(cleaned) <= 4
}

// modelSettingValue 从 DB Setting 表读配置值（organize.go 内用，不走 Handler）
func modelSettingValue(key string) string {
	var s model.Setting
	if err := model.DB.Where("`key` = ?", key).First(&s).Error; err == nil {
		return s.Value
	}
	return ""
}

// executeOrganizeWithConfig 用指定的 OrgConfig 执行整理（转存目录等场景）
func (h *Handler) executeOrganizeWithConfig(cfg *OrgConfig, syncAfter bool) ([]gin.H, []OrganizeResult, error) {
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, nil, err
	}

	logFn := func(msg string) { log.Println(msg) }
	orgResults, successCount := runOrganizeEngineWithConfig(ops, cfg, logFn)

	steps := []gin.H{}
	totalFiles := len(orgResults)
	existsCount, failedCount := 0, 0
	for _, r := range orgResults {
		if r.Status == "exists" {
			existsCount++
		}
		if r.Status == "failed" {
			failedCount++
		}
	}
	steps = append(steps, gin.H{"step": "整理（转存目录）", "status": "完成",
		"message": fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", totalFiles, successCount, existsCount, failedCount)})

	return steps, orgResults, nil
}

// runOrganizeEngineWithConfig 用指定的 OrgConfig 运行整理引擎
func runOrganizeEngineWithConfig(ops *pan115Ops, cfg *OrgConfig, onLog func(string)) ([]OrganizeResult, int) {
	results := []OrganizeResult{}
	successCount := 0

	tc, err := loadTmdbClient(nil)
	if err != nil {
		onLog("✗ TMDB 配置错误: " + err.Error())
		return results, 0
	}

	replaceRules := loadReplaceRules()

	// 计算库根绝对路径（去重记录网盘验证用，不能传空否则验证被跳过）
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library, map[string]dirInfo{})
	}

	topEntries, err := listPendingTopLevel(ops, cfg.Pending)
	if err != nil {
		onLog("✗ 遍历转存目录失败: " + err.Error())
		return results, 0
	}
	if len(topEntries) == 0 {
		onLog("○ 转存目录为空")
		return results, 0
	}

	// 四个工作区根目录自身永不被当作条目处理（与 runOrganizeEngine 一致）
	excluded := map[string]bool{cfg.Library: true, cfg.Existing: true, cfg.Redundant: true, cfg.Pending: true}
	filtered := topEntries[:0]
	for _, e := range topEntries {
		if e.IsDir && excluded[e.Cid] {
			continue
		}
		filtered = append(filtered, e)
	}
	topEntries = filtered

	guards := newOrgGuards(ops.cookie, cfg.Pending, cfg)
	if guards.active {
		onLog("⚠ 扫描根覆盖到媒体库/已存在/冗余目录，这些子树内的条目将被跳过（防误整理库内容）")
	}

	onLog(fmt.Sprintf("▶ 转存目录发现 %d 个条目，开始整理...", len(topEntries)))
	for _, entry := range topEntries {
		results = append(results, processEntry(ops, cfg, tc, replaceRules, guards, entry, libAbs, onLog, 0, &successCount)...)
		time.Sleep(300 * time.Millisecond)
	}
	return results, successCount
}


// ==================== AV 番号识别与处理 ====================

// avCategoryConfig AV 分类配置（与 UI 上的 YAML 对应）
// 无码/有码/未分类 按番号前缀匹配，空前缀 = 兜底
type avCategoryConfig struct {
	Name      string   // 分类名（如"无码"）
	Prefixes  []string // 番号前缀列表（空 = 兜底）
}

// loadAVCategories 从 org-basic 配置读 AV 分类（暂用内置默认，后续接 UI 的 YAML）
func loadAVCategories() []avCategoryConfig {
	// 默认配置（与用户 YAML 示例一致）
	return []avCategoryConfig{
		{Name: "无码", Prefixes: []string{"ABC", "DEF", "START"}},
		{Name: "有码", Prefixes: nil}, // 兜底
	}
}

// classifyAVNumber 按番号前缀分类，返回分类名
func classifyAVNumber(avNum string) string {
	cats := loadAVCategories()
	avNumUpper := strings.ToUpper(avNum)
	// 先精确匹配有前缀的分类
	for _, cat := range cats {
		if len(cat.Prefixes) == 0 {
			continue // 跳过兜底
		}
		for _, prefix := range cat.Prefixes {
			if strings.HasPrefix(avNumUpper, strings.ToUpper(prefix)) {
				return cat.Name
			}
		}
	}
	// 再找兜底分类
	for _, cat := range cats {
		if len(cat.Prefixes) == 0 {
			return cat.Name
		}
	}
	return "未分类"
}

// sanitizeAVFilename 清洗 AV 文件名中的广告前缀
// "4k688.com@START-622.mp4" → "START-622.mp4"
// "www.xxx.com@MIDV-001.mp4" → "MIDV-001.mp4"
func sanitizeAVFilename(name string) string {
	// 保留扩展名，只清洗基名
	ext := pathExt(name)
	base := baseName(name)

	// ===== 1. 网站域名类前缀 =====
	// "4k688.com@XXX-001" / "www.xxx.com@XXX-001" / "xxx.com-XXX-001"
	// @ 分隔符：取 @ 之后
	if idx := strings.LastIndex(base, "@"); idx >= 0 && idx < len(base)/2 {
		base = base[idx+1:]
	}
	// www.xxx.com 前缀
	base = regexp.MustCompile(`(?i)^www\.[a-z0-9.-]+\.(com|net|org|cc|xyz|me|tv|info)[-_.@]?`).ReplaceAllString(base, "")
	// 纯域名前缀（4k688.com、avxxx.net 等）
	base = regexp.MustCompile(`(?i)^[a-z0-9]{2,15}\.(com|net|org|cc|xyz|me|tv|info)[-_.@]?`).ReplaceAllString(base, "")

	// ===== 2. 括号类广告 =====
	// 【高清xxx网】【广告】等全角括号
	base = regexp.MustCompile(`【[^】]*】`).ReplaceAllString(base, "")
	// (www.xxx.com) [4k688.com] 等半角括号（只清开头/结尾的，不清中间的标签）
	base = regexp.MustCompile(`(?i)^\(\s*(www\.)?[a-z0-9.-]+\.(com|net|cc|xyz)\s*\)[-_. ]*`).ReplaceAllString(base, "")
	base = regexp.MustCompile(`(?i)^\[\s*(www\.)?[a-z0-9.-]+\.(com|net|cc|xyz)\s*\][-_. ]*`).ReplaceAllString(base, "")

	// ===== 3. 文字类广告前缀/后缀 =====
	// "高清剧集网"、"破解版"、"完整版"、"中文字幕" 等常见前缀
	adPrefixes := []string{
		"高清剧集网", "高清网站", "最新地址", "永久地址", "官方网址",
		"破解版", "完整版", "无修正版", "高清版", "无码版", "有码版",
		"中文字幕", "无码破解", "字幕版", "4K修复版",
	}
	for _, ad := range adPrefixes {
		if strings.HasPrefix(base, ad) {
			base = strings.TrimPrefix(base, ad)
			// 清除前缀后面的分隔符
			base = strings.TrimLeft(base, "-_. ")
		}
	}

	// ===== 4. URL 参数类后缀 =====
	// "XXX-001?from=4k688" / "XXX-001 - www.4k688.com"
	base = regexp.MustCompile(`[-_ ]+(www\.)?[a-z0-9.-]+\.(com|net|cc|xyz|me|tv)$`).ReplaceAllString(base, "")
	base = regexp.MustCompile(`\?[a-z=&0-9]+$`).ReplaceAllString(base, "")

	// ===== 5. 多余分隔符 =====
	base = strings.Trim(base, "-_. ")
	for strings.Contains(base, "  ") {
		base = strings.ReplaceAll(base, "  ", " ")
	}
	base = strings.Trim(base, "-_. ")

	return base + ext
}

// avNumRegex 匹配常见 AV 番号格式：ABC-123、ABCD-12 等
var avNumRegex = regexp.MustCompile(`(?i)\b([A-Z]{2,6})-?(\d{2,5})\b`)

// fc2NumRegex 匹配 FC2 番号：FC2-PPV-1234567、FC2_1234567 等（数字 5-8 位）
var fc2NumRegex = regexp.MustCompile(`(?i)\bfc2[-_]?(?:ppv[-_]?)?(\d{5,8})\b`)

// avExcludedPrefixes 常见非 AV 前缀（画质/编码/剧集标记），命中则视为误匹配
var avExcludedPrefixes = map[string]bool{
	"THE": true, "AND": true, "FOR": true, "HD": true, "SD": true, "XX": true,
	"WEB": true, "DL": true, "BLU": true, "UHD": true, "HDR": true, "DTS": true,
	"AAC": true, "H264": true, "H265": true, "X264": true, "X265": true,
	"HEVC": true, "AVC": true, "1080P": true, "720P": true, "2160P": true,
	"4K": true, "2K": true, "CD": true, "DVD": true, "BDRIP": true, "HDRIP": true,
	"WP": true, "XXX": true,
	// 剧集/动漫常见标记（ep01、se02、sp01、ova、ncop 等）
	"EP": true, "SE": true, "SP": true, "EPT": true, "OVA": true, "OAD": true,
	"NCOP": true, "NCED": true, "PV": true, "CM": true, "TV": true, "VOL": true,
}

// detectAVNumber 从目录名和文件名中检测 AV 番号
// 优先用目录名（更干净），文件名作为补充；返回规范化的番号（前缀大写）
func detectAVNumber(dirName, fileName string) string {
	for _, s := range []string{dirName, fileName} {
		if s == "" {
			continue
		}
		// FC2 番号优先（通用规则匹配不到 7 位数字）
		if m := fc2NumRegex.FindStringSubmatch(s); m != nil {
			return "FC2-PPV-" + m[1]
		}
		if m := avNumRegex.FindStringSubmatch(s); m != nil {
			prefix := strings.ToUpper(m[1])
			if !avExcludedPrefixes[prefix] {
				return prefix + "-" + m[2]
			}
		}
	}
	return ""
}

// avAdDomainRegex 广告域名（清洗后仍任意位置出现即视为广告载体）
var avAdDomainRegex = regexp.MustCompile(`(?i)(?:https?://|www\.)?[a-z0-9][a-z0-9-]{1,15}\.(?:com|net|org|cc|xyz|me|tv|info|vip|top|app|club|site|online|icu|fun|win)\b`)

// avAdKeywords 广告文件常见关键词
var avAdKeywords = []string{
	"18+", "游戏大全", "最新地址", "永久地址", "永久导航", "网址导航", "导航网",
	"发布页", "发布器", "天天更新", "每周更新", "免费观看", "在线观看", "手机看片",
	"福利网", "福利社", "破解版", "高清资源网", "资源网", "看片网", "影片网",
	"电影网", "安卓版", "app版", "app下载", "磁力搜索", "同城约",
}

// isAVAdFile 判断 AV 目录内的文件是否为广告/引流文件。
// 以「清洗后的文件名」为准：正规片的广告前缀会被 sanitizeAVFilename 清掉
// （"4k688.com@START-622.mp4" → "START-622.mp4"，不含域名，正常入库）；
// 清完仍残留域名或广告词的才是真广告（如 "18+游戏大全(996gg.cc)-…"）
func isAVAdFile(name string) bool {
	cleaned := baseName(sanitizeAVFilename(name))
	if avAdDomainRegex.MatchString(cleaned) {
		return true
	}
	lower := strings.ToLower(cleaned)
	for _, kw := range avAdKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// avSubLangRegex 字幕语言标记（.chs/.cht/.eng 等）
var avSubLangRegex = regexp.MustCompile(`\.(chs|cht|eng|chi|jap)\b`)

// avCarriesNumber 正片文件名（清洗后）应携带番号，不带番号的多为引流视频；
// FC2 文件名常省略 PPV 段（FC2-1234567 / FC2-PPV-1234567 两种写法都认），
// 连字符差异（START622 / START-622）也容忍
func avCarriesNumber(cleanedBase, avNum string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }
	lc := norm(cleanedBase)
	lcc := strings.ReplaceAll(lc, "-", "")
	for _, c := range []string{norm(avNum), strings.ReplaceAll(norm(avNum), "-ppv", "")} {
		if c != "" && (strings.Contains(lc, c) || strings.Contains(lcc, strings.ReplaceAll(c, "-", ""))) {
			return true
		}
	}
	return false
}

// processAVDirectory 处理 AV 目录（跳过 TMDB，直接用番号入库）
func processAVDirectory(ops *pan115Ops, cfg *OrgConfig, media *TmdbMedia, dir dirEntry, files []remoteFile, onLog func(string), results []OrganizeResult) []OrganizeResult {
	// AV 目录结构：/AV/分类/番号/（分类 = 无码/有码，按番号前缀匹配）
	category := classifyAVNumber(media.Title)
	avDir := category + "/" + media.Title

	// 分流：正片视频（含多分卷）/ 字幕 / 元数据 / 广告垃圾
	var videos, subs, metas []remoteFile
	var junkFids []string
	minBytes := int64(cfg.MinSize) * 1024 * 1024
	for _, f := range files {
		switch classifyFile(f.Name) {
		case FileTypeVideo:
			cleanedBase := baseName(sanitizeAVFilename(f.Name))
			switch {
			case isAVAdFile(f.Name):
				junkFids = append(junkFids, f.Fid)
				onLog(fmt.Sprintf("○ %s - 广告/引流视频，移到冗余", f.Name))
			case !avCarriesNumber(cleanedBase, media.Title):
				junkFids = append(junkFids, f.Fid)
				onLog(fmt.Sprintf("○ %s - 清洗后不含番号 %s，疑似引流视频，移到冗余", f.Name, media.Title))
			case minBytes > 0 && f.Size > 0 && f.Size < minBytes:
				junkFids = append(junkFids, f.Fid)
				onLog(fmt.Sprintf("○ %s - 仅 %.1fMB（小于最小体积 %dMB），移到冗余",
					f.Name, float64(f.Size)/1024/1024, cfg.MinSize))
			default:
				videos = append(videos, f)
			}
		case FileTypeSubtitle:
			subs = append(subs, f)
		case FileTypeNFO, FileTypeStdImage:
			metas = append(metas, f)
		default:
			junkFids = append(junkFids, f.Fid)
			onLog(fmt.Sprintf("○ %s - 垃圾文件，移到冗余", f.Name))
		}
	}

	// 正片全被过滤（整包都是广告）→ 整目录进冗余
	if len(videos) == 0 {
		if err := ops.moveFiles(cfg.Redundant, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到冗余失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ - 无有效视频（全部为广告/垃圾），已移到冗余", dir.Name))
		}
		return results
	}

	// 广告/垃圾先行移到冗余
	if len(junkFids) > 0 {
		if err := ops.moveFiles(cfg.Redundant, junkFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 垃圾文件移到冗余失败: %v", dir.Name, err))
		}
	}

	onLog(fmt.Sprintf("▣ AV 目标目录: %s（分类: %s）", avDir, category))
	targetCid, err := ops.ensurePath(cfg.Library, avDir)
	if err != nil {
		onLog(fmt.Sprintf("✗ AV 创建目录失败: %v", err))
		return results
	}

	// 重命名：正片按体积降序，主片 = 番号.ext，其余分卷 = 番号-CDn.ext
	// （广告包里常见"引流视频+正片"，过去全部重命名为番号导致同名冲突）
	sort.Slice(videos, func(i, j int) bool { return videos[i].Size > videos[j].Size })
	renameTo := func(fid, oldName, newName string) {
		if newName == oldName {
			return
		}
		if err := ops.rename(fid, newName); err != nil {
			onLog(fmt.Sprintf("○ AV 重命名失败保持原名 %s: %v", oldName, err))
		} else {
			onLog(fmt.Sprintf("✓ AV 清洗重命名 %s → %s", oldName, newName))
		}
	}
	var moveFids []string
	for i, v := range videos {
		newName := media.Title + pathExt(v.Name)
		if i > 0 {
			newName = fmt.Sprintf("%s-CD%d%s", media.Title, i+1, pathExt(v.Name))
		}
		renameTo(v.Fid, v.Name, newName)
		moveFids = append(moveFids, v.Fid)
	}
	for _, s := range subs {
		subSuffix := ""
		if m := avSubLangRegex.FindStringSubmatch(baseName(s.Name)); m != nil {
			subSuffix = m[0]
		}
		newName := media.Title + subSuffix + pathExt(s.Name)
		renameTo(s.Fid, s.Name, newName)
		moveFids = append(moveFids, s.Fid)
	}
	for _, m := range metas {
		newName := media.Title + pathExt(m.Name)
		renameTo(m.Fid, m.Name, newName)
		moveFids = append(moveFids, m.Fid)
	}

	if err := ops.moveFiles(targetCid, moveFids); err != nil {
		onLog(fmt.Sprintf("✗ AV 移动失败: %v", err))
		return results
	}

	// 移空的源目录到冗余
	ops.moveFiles(cfg.Redundant, []string{dir.Fid})

	onLog(fmt.Sprintf("✓ AV 入库: %s → %s", dir.Name, avDir))
	for _, v := range videos {
		results = append(results, OrganizeResult{
			FileName: v.Name, Status: "success",
			Title: media.Title, MediaType: "av",
			Category: category, TargetDir: avDir,
			Message: fmt.Sprintf("AV %s/%s", category, media.Title),
		})
	}
	return results
}
