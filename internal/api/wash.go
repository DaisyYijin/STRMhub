package api

// ==================== 洗版策略（版本比较替换）====================
//
// 命中已存在时不再一律移「已存在」：
//   1. 取库内该片的现有文件名（SyncedFile 台账，记录在 TargetPath 之下）
//   2. 与待整理文件按优先级规则逐条比较（制作组/分辨率/来源/效果）
//   3. 新版更好 → 旧版移冗余（洗版-旧版本/片名），新版正常入库（replace）
//      旧版更好 → 新版移已存在（现状）；规则无判定 → 按 coexist 也移已存在

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"strmhub/internal/model"
)

// washRule 单条优先级规则（与 WashRule.PriorityLevel 的 JSON 数组元素对应）
type washRule struct {
	ResourceTeam    string `json:"resource_team"`
	ResourcePix     string `json:"resource_pix"`
	ResourceType    string `json:"resource_type"`
	ResourceEffect  string `json:"resource_effect"`
}

// loadWashRules 加载匹配的洗版策略（media_type/category 任一匹配；空=匹配所有）
func loadWashRules(mediaType, category string) []washRule {
	var rows []model.WashRule
	model.DB.Find(&rows)
	for _, r := range rows {
		if r.MediaType != "" && r.MediaType != mediaType {
			continue
		}
		if r.Category != "" && !containsCategory(r.Category, category) {
			continue
		}
		var rules []washRule
		if json.Unmarshal([]byte(r.PriorityLevel), &rules) == nil && len(rules) > 0 {
			return rules
		}
	}
	return nil
}

func containsCategory(list, cat string) bool {
	for _, c := range strings.Split(list, ",") {
		if strings.TrimSpace(c) == cat {
			return true
		}
	}
	return false
}

// ruleMatch 判断文件名是否满足规则条件（字段为"!"前缀表示排除）
func ruleMatch(name string, r washRule) bool {
	return matchField(name, r.ResourceTeam, extractTeam(name)) &&
		matchField(name, r.ResourcePix, extractPix(name)) &&
		matchField(name, r.ResourceType, extractType(name)) &&
		matchField(name, r.ResourceEffect, extractEffect(name))
}

// matchField 单字段匹配：空=不限；!xxx=不含；xxx=包含
func matchField(name, cond, value string) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true
	}
	neg := strings.HasPrefix(cond, "!")
	want := strings.TrimPrefix(cond, "!")
	hit := strings.Contains(strings.ToLower(name), strings.ToLower(want)) || strings.Contains(strings.ToLower(value), strings.ToLower(want))
	return neg != hit
}


// 画质提取已迁移到 resource.go 的 ParseResourceInfo（完整版）
func extractPix(name string) string   { return ParseResourceInfo(name).Pix }
func extractType(name string) string  { return ParseResourceInfo(name).Type }
func extractEffect(name string) string { return ParseResourceInfo(name).Effect }
func extractTeam(name string) string  { return ParseResourceInfo(name).Team }

// washDecision 洗版判定：返回是否替换（新版本优于库内版本）
func washDecision(newName string, libraryNames []string, rules []washRule) bool {
	if len(rules) == 0 || len(libraryNames) == 0 {
		return false
	}
	oldName := libraryNames[0]
	for _, r := range rules {
		newHit := ruleMatch(newName, r)
		oldHit := ruleMatch(oldName, r)
		if newHit != oldHit {
			return newHit // 首条能分出高下的规则决定胜负
		}
	}
	return false // 规则无法判定
}

// libraryFileNamesOf 从台账取某片目录下的现有文件名
func libraryFileNamesOf(targetDir string) []string {
	var sfs []model.SyncedFile
	prefix := strings.TrimSuffix(targetDir, "/") + "/"
	model.DB.Where("rel_path LIKE ?", prefix+"%").Limit(20).Find(&sfs)
	names := make([]string, 0, len(sfs))
	for _, sf := range sfs {
		names = append(names, path.Base(sf.RelPath))
	}
	return names
}


// lookupMediaRecord 查库内整理记录（命中返回记录）
func lookupMediaRecord(media *TmdbMedia) (*model.MediaLibrary, bool) {
	var rec model.MediaLibrary
	if err := model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).First(&rec).Error; err != nil {
		return nil, false
	}
	return &rec, true
}

// tryWashReplace 洗版判定与替换执行：新版更好时把库内旧版移到冗余并
// 清理台账/记录，返回 true 让调用方继续正常入库；否则返回 false
func tryWashReplace(ops *pan115Ops, cfg *OrgConfig, media *TmdbMedia, newName, targetDir string, onLog func(string)) bool {
	rules := loadWashRules(media.MediaType, "")
	if len(rules) == 0 {
		return false
	}
	libNames := libraryFileNamesOf(targetDir)
	if len(libNames) == 0 {
		return false
	}
	if !washDecision(newName, libNames, rules) {
		onLog(fmt.Sprintf("○ 洗版判定: %s 不优于库内版本，按已存在处理", newName))
		return false
	}
	// 新版更好：旧版文件移冗余（洗版-旧版本/片名 子目录）
	var sfs []model.SyncedFile
	prefix := strings.TrimSuffix(targetDir, "/") + "/"
	model.DB.Where("rel_path LIKE ?", prefix+"%").Find(&sfs)
	fids := make([]string, 0, len(sfs))
	for _, sf := range sfs {
		fids = append(fids, sf.FileID)
	}
	if len(fids) > 0 {
		oldDir := path.Dir(targetDir)
		junkCid, err := ops.ensurePath(cfg.Redundant, "洗版-旧版本/"+path.Base(oldDir))
		if err == nil {
			if err := ops.moveFiles(junkCid, fids); err != nil {
				onLog(fmt.Sprintf("✗ 洗版移动旧版失败: %v", err))
				return false
			}
		}
		model.DB.Where("rel_path LIKE ?", prefix+"%").Delete(&model.SyncedFile{})
	}
	model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).Delete(&model.MediaLibrary{})
	onLog(fmt.Sprintf("✦ 洗版替换: %s 优于库内版本（%s），旧版已移到冗余", newName, libNames[0]))
	return true
}
