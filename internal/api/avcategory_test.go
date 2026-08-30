package api

import "testing"

func TestParseAVCategoriesFromYAML(t *testing.T) {
	src := `movie:
  电影/外语电影:
av:
  无码:
    num_prefix: 'FC2,HEYZO'
  国产:
    num_prefix: 'MD,PMC'
  有码:
    num_prefix: ''
`
	cats := parseAVCategoriesFromYAML(src)
	if len(cats) != 3 {
		t.Fatalf("want 3 av categories, got %d", len(cats))
	}
	if cats[0].Name != "无码" || len(cats[0].Prefixes) != 2 {
		t.Errorf("无码 parse wrong: %+v", cats[0])
	}
	if len(cats[2].Prefixes) != 0 {
		t.Errorf("有码 应为兜底（空前缀）: %+v", cats[2])
	}
	// 无 av 段
	if got := parseAVCategoriesFromYAML("movie:\n  电影/外语电影:\n"); got != nil {
		t.Errorf("无 av 段应返回 nil, got %v", got)
	}
}

func TestClassifyAVNumber(t *testing.T) {
	// 测试用固定分类（绕过 DB 读取）：直接构造分类切片走同序逻辑
	// classifyAVNumber 内部 loadAVCategories 走 DB——DB 未初始化时
	// 回退默认（无码/有码），恰好可用于测内置库与兜底
	cases := []struct {
		num, hint, want string
	}{
		{"FC2-PPV-123", "", "无码"},          // 内置无码库
		{"HEYZO-2345", "", "无码"},            // 内置无码库
		{"n1093", "", "无码"},                 // Tokyo Hot（N10 前缀）
		{"MDX-005", "", "有码"},               // 内置国产库 → 默认配置无"国产"分类 → 落兜底有码
		{"MIDA-732", "【某某】MIDA-732 无码破解", "无码"}, // 文件名关键词
		{"ABC-123", "", "有码"},               // 兜底
		{"START-620", "", "有码"},             // 兜底（START 不再是无码内置）
	}
	for _, c := range cases {
		if got := classifyAVNumber(c.num, c.hint); got != c.want {
			t.Errorf("classify(%q, %q) = %q, want %q", c.num, c.hint, got, c.want)
		}
	}
}
