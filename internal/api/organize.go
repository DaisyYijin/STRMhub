package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"strmhub/internal/model"
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

// RenameConfig 重命名配置
type RenameConfig struct {
	MovieFolder string `json:"movie_folder"` // 电影文件夹命名规则
	MovieFile   string `json:"movie_file"`   // 电影文件命名规则
	TVFolder    string `json:"tv_folder"`    // 电视剧文件夹命名规则
	TVFile      string `json:"tv_file"`      // 电视剧文件命名规则
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

func buildNewName(media *TmdbMedia, parsed *ParsedName, ext string) string {
	media.Title = sanitizeName(media.Title)
	// 首字母
	firstLetter := "0"
	if len(media.Title) > 0 {
		ch := media.Title[0]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			firstLetter = strings.ToUpper(string(ch))
		} else if ch >= '0' && ch <= '9' {
			firstLetter = "#"
		}
	}

	if media.MediaType == "movie" {
		// 电影：{first_letter}/{title} ({year}) [{tmdb_id}]/{title} ({year}) [{tmdb_id}].{ext}
		folder := fmt.Sprintf("%s/%s (%s) [%d]", firstLetter, media.Title, media.Year, media.TmdbID)
		file := fmt.Sprintf("%s (%s) [%d]%s", media.Title, media.Year, media.TmdbID, ext)
		return folder + "/" + file
	}

	// 电视剧：{first_letter}/{title} ({year}) [{tmdb_id}]/Season {season}/{title} - S{season:02d}E{episode:02d}.{ext}
	folder := fmt.Sprintf("%s/%s (%s) [%d]", firstLetter, media.Title, media.Year, media.TmdbID)
	if parsed.Season > 0 {
		subFolder := fmt.Sprintf("Season %02d", parsed.Season)
		if parsed.Episode > 0 {
			file := fmt.Sprintf("%s - S%02dE%02d%s", media.Title, parsed.Season, parsed.Episode, ext)
			return folder + "/" + subFolder + "/" + file
		}
		return folder + "/" + subFolder
	}
	return folder
}

// checkExists 检查是否已存在（通过 TMDB ID 在数据库中比对）
func checkExists(media *TmdbMedia) bool {
	var count int64
	model.DB.Model(&model.MediaLibrary{}).Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).Count(&count)
	return count > 0
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
			e := dirEntry{
				Fid:  fmt.Sprint(d["fid"]),
				Name: fmt.Sprint(d["n"]),
				IsDir: isDir,
				Cid:  fmt.Sprint(d["cid"]),
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
				files = append(files, remoteFile{
					Fid:  fmt.Sprint(d["fid"]),
					Name: name,
					Path: basePath,
					Size: size,
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

	onLog(fmt.Sprintf("发现 %d 个条目，开始整理...", len(topEntries)))

	for _, entry := range topEntries {
		if entry.IsDir {
			// 子目录：收集所有文件，找到视频进行识别
			subFiles, err := collectDirFiles(ops, entry.Cid, entry.Name)
			if err != nil {
				onLog(fmt.Sprintf("✗ %s/ - 遍历失败: %v", entry.Name, err))
				continue
			}
			if len(subFiles) == 0 {
				continue
			}
			result := processDir(ops, cfg, tc, replaceRules, entry, subFiles, onLog)
			results = append(results, result...)
			for _, r := range result {
				if r.Status == "success" {
					successCount++
				}
			}
		} else {
			// 顶层直接是文件
			if classifyFile(entry.Name) != FileTypeVideo {
				continue // 跳过非视频文件
			}
			// 过滤小文件
			if cfg.MinSize > 0 && entry.Size > 0 && entry.Size < cfg.MinSize*1024*1024 {
				continue
			}
			f := remoteFile{Fid: entry.Fid, Name: entry.Name, Size: entry.Size}
			result := processSingleFile(ops, cfg, tc, replaceRules, f, onLog)
			results = append(results, result)
			if result.Status == "success" {
				successCount++
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	return results, successCount
}

// processDir 处理一个子目录（包含多个文件的影视目录）
func processDir(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, dir dirEntry, files []remoteFile, onLog func(string)) []OrganizeResult {
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
	parsed := parseFileName(name)
	if parsed.Title == "" {
		// 无法识别，整个目录移到冗余
		moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
		onLog(fmt.Sprintf("○ %s/ - 无法提取标题，已移到冗余", dir.Name))
		return results
	}

	// TMDB 识别
	media, err := tc.recognize(parsed)
	if err != nil || media == nil {
		msg := "TMDB 未找到匹配"
		if err != nil {
			msg = "TMDB 识别失败: " + err.Error()
		}
		moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
		onLog(fmt.Sprintf("○ %s/ - %s，已移到冗余", dir.Name, msg))
		return results
	}

	// 检查是否已存在
	if checkExists(media) {
		// 已存在 → 移到"已经存在"目录
		if err := ops.moveFiles(cfg.Existing, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到已存在失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ → 已存在: %s (%s)，已移到已存在目录", dir.Name, media.Title, media.Year))
		}
		// 为每个视频文件生成结果
		for _, vf := range videoFiles {
			results = append(results, OrganizeResult{
				FileName:  vf.Name,
				Status:    "exists",
				Title:     media.Title,
				Year:      media.Year,
				MediaType: media.MediaType,
				Message:   fmt.Sprintf("已存在: %s (%s)", media.Title, media.Year),
			})
		}
		return results
	}

	// 不存在 → 分类 + 移动到我的影视库
	category := classifyMedia(media)
	ext := pathExt(mainVideo.Name)
	newPath := buildNewName(media, parsed, ext)
	targetDir := category + "/" + pathDir(newPath)

	// 在我的影视库创建目标目录
	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		msg := fmt.Sprintf("创建目录失败: %v（分类=%q 目标目录=%q）", err, category, targetDir)
		onLog(fmt.Sprintf("✗ %s/ - %s", dir.Name, msg))
		results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: msg})
		return results
	}

	// 按文件分类移动
	var keepFids, junkFids []string
	for _, f := range files {
		ft := classifyFile(f.Name)
		if ft == FileTypeJunk {
			junkFids = append(junkFids, f.Fid)
		} else {
			keepFids = append(keepFids, f.Fid)
		}
	}

	// 移动保留文件到影视库
	if len(keepFids) > 0 {
		if err := ops.moveFiles(targetCid, keepFids); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动文件失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "移动文件失败: " + err.Error()})
			return results
		}
	}

	// 移动无用文件到冗余
	if len(junkFids) > 0 {
		junkCid, err := ops.ensurePath(cfg.Redundant, dir.Name)
		if err == nil {
			ops.moveFiles(junkCid, junkFids)
		}
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

// processSingleFile 处理待整理目录下的顶层单独视频文件
func processSingleFile(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, onLog func(string)) OrganizeResult {
	result := OrganizeResult{FileName: f.Name}

	// 应用替换规则
	name := f.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
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

	// 检查是否已存在
	if checkExists(media) {
		ops.moveFiles(cfg.Existing, []string{f.Fid})
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Existing, false, onLog)
		result.Status = "exists"
		result.Message = fmt.Sprintf("已存在: %s (%s)，已移到已存在目录", media.Title, media.Year)
		onLog(fmt.Sprintf("○ %s → 已存在: %s (%s)", f.Name, media.Title, media.Year))
		return result
	}

	// 分类 + 移动到影视库
	category := classifyMedia(media)
	ext := pathExt(f.Name)
	newPath := buildNewName(media, parsed, ext)
	targetDir := category + "/" + pathDir(newPath)

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
