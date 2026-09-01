package api

import (
	"encoding/json"
	"testing"
)

// 推荐序排序：115 优先于夸克、免费优先、4K 优先（对齐 agentresourceofficer resource_sort_key）
func TestHdhiveResourceLess(t *testing.T) {
	mk := func(pan string, points int64, res []any, src []any) map[string]any {
		m := map[string]any{"title": "x", "pan_type": pan, "unlock_points": float64(points)}
		if res != nil {
			m["video_resolution"] = res
		}
		if src != nil {
			m["source"] = src
		}
		return m
	}
	cases := []struct {
		name string
		a, b map[string]any
		want bool
	}{
		{"115 在前", mk("115", 0, nil, nil), mk("quark", 0, nil, nil), true},
		{"免费在前", mk("115", 5, nil, nil), mk("115", 0, nil, nil), false},
		{"4K 在前", mk("115", 0, []any{"4K"}, nil), mk("115", 0, []any{"1080P"}, nil), true},
		{"蓝光原盘在前", mk("115", 0, nil, []any{"蓝光原盘/REMUX"}), mk("115", 0, nil, []any{"WEB-DL/WEBRip"}), true},
	}
	for _, tc := range cases {
		if got := hdhiveResourceLess(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// data 兼容数组与 {resources:[...]} 两种结构；排序后 115 免费项置顶
func TestHdhiveParseResourceList(t *testing.T) {
	arr := json.RawMessage(`[{"slug":"b","pan_type":"quark"},{"slug":"a","pan_type":"115"}]`)
	items := hdhiveParseResourceList(arr)
	if len(items) != 2 || items[0]["slug"] != "b" {
		t.Fatalf("数组结构解析失败: %v", items)
	}
	obj := json.RawMessage(`{"resources":[{"slug":"a","pan_type":"115"}],"total":1}`)
	items = hdhiveParseResourceList(obj)
	if len(items) != 1 || items[0]["slug"] != "a" {
		t.Fatalf("对象结构解析失败: %v", items)
	}
}

// unlock 响应字段多版本兼容 + 链接内嵌提取码
func TestHdhiveExtractShareLink(t *testing.T) {
	link, pass := hdhiveExtractShareLink(map[string]any{
		"full_link": "https://115.com/s/arvYE?password=a1b2",
	})
	if link != "https://115.com/s/arvYE?password=a1b2" || pass != "a1b2" {
		t.Fatalf("full_link/内嵌提取码解析失败: %q %q", link, pass)
	}
	link, pass = hdhiveExtractShareLink(map[string]any{
		"url":         "https://115.com/s/xyz",
		"access_code": "cc",
	})
	if link != "https://115.com/s/xyz" || pass != "cc" {
		t.Fatalf("url/access_code 解析失败: %q %q", link, pass)
	}
	if l, _ := hdhiveExtractShareLink(map[string]any{"foo": "bar"}); l != "" {
		t.Fatalf("无关字段不应解析出链接: %q", l)
	}
}
