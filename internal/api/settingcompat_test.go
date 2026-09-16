package api

import (
	"os"
	"testing"

	"strmhub/internal/config"
	"strmhub/internal/model"
)

// 回归测试：配置保存在 YAML（前端 SaveSetting 的落点），
// 服务端读取必须 YAML 优先——否则整理时的替换规则/重命名模板等永远读不到（空配置）。
func TestSettingValueCompatYAMLFirst(t *testing.T) {
	defer func() { notifyConfigSource = nil; model.DB = nil }()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	// notifyConfigSource 无 nil DB 时也不许 panic
	if settingValueCompat("nothing") != "" {
		t.Error("empty source should return empty")
	}
	if err := notifyConfigSource.SaveSetting("org-recognize", `{"replace_rules":"a=>b"}`); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if got := settingValueCompat("org-recognize"); got != `{"replace_rules":"a=>b"}` {
		t.Errorf("settingValueCompat(org-recognize) = %q, want yaml value", got)
	}
	// loadReplaceRules 应能从 YAML 解析出规则（replace_rules 是规则数组的 JSON 字符串）
	cfg := `{"replace_rules":"[{\"from\":\"旧名\",\"to\":\"新名\"}]"}`
	if err := notifyConfigSource.SaveSetting("org-recognize", cfg); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	rules := loadReplaceRules()
	if len(rules) == 0 {
		t.Fatal("loadReplaceRules returned no rules from YAML config")
	}
	if rules[0].From != "旧名" || rules[0].To != "新名" {
		t.Errorf("rule = %+v, want 旧名=>新名", rules[0])
	}
	_ = os.RemoveAll(notifyConfigSource.DataDir)
}

func TestGetStrmConfigYAMLFirst(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{DataDir: tempDir, ConfigDir: tempDir}
	h := &Handler{Config: cfg}

	// 默认未配置时：返回默认兜底域名
	domain, format, keepExt, skipExist := h.getStrmConfig()
	if domain != "http://172.17.0.1:6086" || format != "pick_code_name" || !keepExt || skipExist {
		t.Errorf("default getStrmConfig() = (%q, %q, %v, %v), want defaults", domain, format, keepExt, skipExist)
	}

	// 用户在前端通过 SaveSetting 保存到 YAML 中
	strmJSON := `{"domain":"http://192.168.1.200:6086","format":"pick_code","keep_ext":false,"exist":"skip"}`
	if err := cfg.SaveSetting("strm", strmJSON); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// 再次调用 getStrmConfig() 必须正确读取到 YAML 配置
	domain, format, keepExt, skipExist = h.getStrmConfig()
	if domain != "http://192.168.1.200:6086" {
		t.Errorf("domain = %q, want http://192.168.1.200:6086", domain)
	}
	if format != "pick_code" {
		t.Errorf("format = %q, want pick_code", format)
	}
	if keepExt {
		t.Errorf("keepExt = true, want false")
	}
	if !skipExist {
		t.Errorf("skipExist = false, want true")
	}
}
