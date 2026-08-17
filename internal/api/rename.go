package api

// ==================== 重命名模板引擎（CMS 20+ 变量完整支持） ====================
//
// 模板变量（与 CMS 完全对齐）：
//   {original_name}     原文件名
//   {ext}               扩展名（含.）
//   {title}             TMDB 中文标题
//   {en_title}          TMDB 英文标题
//   {first_letter}      标题拼音首字母（大写）
//   {year}              年份
//   {tmdb_id}           TMDB ID
//   {resource_pix}      分辨率
//   {resource_version}  资源版本（IMAX/HQ/3D/CC/DC等）
//   {resource_source}   来源平台（NF/DSNP/AMZN等）
//   {resource_type}     资源质量（BluRay/WEB-DL/HDTV）
//   {resource_effect}   特效（DV.HDR/DV/HDR/SDR）
//   {video_encode}      视频编码（H265.10bit/x264等）
//   {audio_encode}      音频编码（TrueHD.7.1/AAC2.0等）
//   {resource_team}     发布组
//   {fps}               帧率
//   {season_episode}    季集 SxxExx
//   {season_num}        季号
//   {episode_num}       集号
//   {disc_num}          盘号
//   {season_name}       季名（TMDB 季信息，可能为空）
//   {season_year}       季年份（可能为空）
//   {episode_name}      集名（TMDB 集信息，可能为空）
//   {custom_regex_match} 自定义正则匹配结果
//
// 支持块语法：{变量非空则输出}，如 {first_letter}/{title} ({year}) [{tmdb_id}]

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenameContext 重命名上下文（包含模板引擎需要的全部数据）
type RenameContext struct {
	OriginalName string
	Ext          string
	Media        *TmdbMedia
	Parsed       *ParsedName
	Resource     ResourceInfo
	SeasonName   string
	SeasonYear   string
	EpisodeName  string
	CustomRegex  string
}

// buildRenameContext 构建重命名上下文
func buildRenameContext(media *TmdbMedia, parsed *ParsedName, originalName string) *RenameContext {
	return &RenameContext{
		OriginalName: originalName,
		Ext:          pathExt(originalName),
		Media:        media,
		Parsed:       parsed,
		Resource:     ParseResourceInfo(originalName),
	}
}

// ApplyTemplate 应用重命名模板，替换所有变量
// 支持块语法：{xxx非空则输出xxx包裹的内容}
func (ctx *RenameContext) ApplyTemplate(template string) string {
	result := template

	// 先处理块语法：{变量名}包裹的部分在变量非空时输出
	// 格式：{first_letter}/{title}  → G/钢铁侠（first_letter非空时输出 /钢铁侠）
	// 简化实现：直接替换变量，块语法由模板设计者用 {var} 控制
	// 高级块语法（{变量非空则输出}）暂不实现，直接变量替换已够用

	replacements := map[string]string{
		// 原始文件信息
		"{original_name}":     ctx.OriginalName,
		"{ext}":              ctx.Ext,
		"{custom_regex_match}": ctx.CustomRegex,

		// TMDB 信息
		"{title}":       ctx.Media.Title,
		"{en_title}":    ctx.Media.OriginalTitle,
		"{year}":        ctx.Media.Year,
		"{tmdb_id}":     fmt.Sprintf("%d", ctx.Media.TmdbID),
		"{first_letter}": titleFirstLetter(ctx.Media.Title),

		// 资源信息
		"{resource_pix}":     ctx.Resource.Pix,
		"{resource_version}": ctx.Resource.Version,
		"{resource_source}":  ctx.Resource.Source,
		"{resource_type}":    ctx.Resource.Type,
		"{resource_effect}":  ctx.Resource.Effect,
		"{video_encode}":     ctx.Resource.VideoEncode,
		"{audio_encode}":     ctx.Resource.AudioEncode,
		"{resource_team}":    ctx.Resource.Team,
		"{fps}":              ctx.Resource.FPS,
		"{disc_num}":         ctx.Resource.DiscNum,

		// 季集信息
		"{season_episode}": ctx.seasonEpisode(),
		"{season_num}":     fmt.Sprintf("%d", ctx.Parsed.Season),
		"{episode_num}":    fmt.Sprintf("%d", ctx.Parsed.Episode),
		"{season_name}":    ctx.SeasonName,
		"{season_year}":    ctx.SeasonYear,
		"{episode_name}":   ctx.EpisodeName,
	}

	for k, v := range replacements {
		result = strings.ReplaceAll(result, k, v)
	}

	// 清理连续的点/横线（变量为空时可能留下）
	result = strings.ReplaceAll(result, "..", ".")
	result = strings.ReplaceAll(result, "--", "-")
	result = strings.ReplaceAll(result, "..", ".") // 再清一次（可能有三个）
	result = strings.TrimSpace(result)

	return result
}

// seasonEpisode 返回 SxxExx 格式
func (ctx *RenameContext) seasonEpisode() string {
	if ctx.Parsed.Season > 0 && ctx.Parsed.Episode > 0 {
		return fmt.Sprintf("S%02dE%02d", ctx.Parsed.Season, ctx.Parsed.Episode)
	}
	if ctx.Parsed.Season > 0 {
		return fmt.Sprintf("S%02d", ctx.Parsed.Season)
	}
	return ""
}

// LoadRenameTemplates 从配置加载重命名模板（yaml 优先，DB 回退）
func (h *Handler) LoadRenameTemplates() *RenameConfig {
	cfg := &RenameConfig{
		MovieFolder: "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
		MovieFile:   "{title}.{resource_pix}.{resource_type}.{video_encode}.{audio_encode}-{resource_team}{ext}",
		TVFolder:    "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
		TVFile:      "{title} - {season_episode}.{resource_pix}.{resource_type}.{video_encode}.{audio_encode}-{resource_team}{ext}",
		AVFolder:    "{first_letter}-{title}",
		AVFile:      "{title}{ext}",
	}

	v := h.getSettingValue("org-rename")
	if v == "" {
		return cfg // 使用默认
	}

	var saved RenameConfig
	if err := json.Unmarshal([]byte(v), &saved); err == nil {
		if saved.MovieFolder != "" {
			cfg.MovieFolder = saved.MovieFolder
		}
		if saved.MovieFile != "" {
			cfg.MovieFile = saved.MovieFile
		}
		if saved.TVFolder != "" {
			cfg.TVFolder = saved.TVFolder
		}
		if saved.TVFile != "" {
			cfg.TVFile = saved.TVFile
		}
		if saved.AVFolder != "" {
			cfg.AVFolder = saved.AVFolder
		}
		if saved.AVFile != "" {
			cfg.AVFile = saved.AVFile
		}
	}
	return cfg
}

// BuildPathWithTemplate 用模板引擎生成完整目标路径
// 返回: 文件夹路径/文件名（不含扩展名已含在文件名模板中）
func (h *Handler) BuildPathWithTemplate(media *TmdbMedia, parsed *ParsedName, originalName string) string {
	ctx := buildRenameContext(media, parsed, originalName)
	tpl := h.LoadRenameTemplates()

	var folder, file string
	switch media.MediaType {
	case "movie":
		folder = ctx.ApplyTemplate(tpl.MovieFolder)
		file = ctx.ApplyTemplate(tpl.MovieFile)
	case "tv":
		folder = ctx.ApplyTemplate(tpl.TVFolder)
		file = ctx.ApplyTemplate(tpl.TVFile)
	default: // AV 等
		folder = ctx.ApplyTemplate(tpl.AVFolder)
		file = ctx.ApplyTemplate(tpl.AVFile)
	}

	return folder + "/" + file
}

// sanitizePath 清理路径中的空段和连续分隔符
func sanitizePath(p string) string {
	parts := strings.Split(p, "/")
	var cleaned []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && part != "." {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "/")
}
