package api

import (
	"os"
	"testing"
)

// 渲染页解析：3 张资源卡片 + 普通链接过滤 + 字段提取
func TestHdhiveParseCards(t *testing.T) {
	raw, err := os.ReadFile("testdata/hd_search.html")
	if err != nil {
		t.Fatal(err)
	}
	cards := hdhiveParseCards(string(raw))
	if len(cards) != 3 {
		t.Fatalf("应解析出 3 张卡片，得到 %d: %+v", len(cards), cards)
	}
	if cards[0].Slug != "abc123" || !cards[0].IsFree {
		t.Errorf("第一张卡 slug/免费标记错误: %+v", cards[0])
	}
	if cards[0].Size != "45.2 GB" || cards[0].Resolution != "4K" {
		t.Errorf("第一张卡大小/分辨率错误: %+v", cards[0])
	}
	if cards[1].UnlockPoints != 5 || cards[1].IsFree {
		t.Errorf("第二张卡积分错误: %+v", cards[1])
	}
	if cards[2].PostedAt != "2024/12/30" {
		t.Errorf("第三张卡发布时间错误: %+v", cards[2])
	}
	if !contains(cards[0].Title, "阿凡达") || !contains(cards[0].Title, "REMUX") {
		t.Errorf("标题提取错误: %q", cards[0].Title)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 推荐序：免费优先、4K 优先
func TestHdhiveCardLess(t *testing.T) {
	free4k := hdhiveCard{Title: "a", IsFree: true, Resolution: "4K", Size: "45 GB"}
	paid1080 := hdhiveCard{Title: "b", IsFree: false, Resolution: "1080P", Size: "8 GB", UnlockPoints: 5}
	if !hdhiveCardLess(free4k, paid1080) {
		t.Error("免费 4K 应排在付费 1080P 前")
	}
	if hdhiveCardLess(paid1080, free4k) {
		t.Error("排序应满足全序")
	}
	big := hdhiveCard{Title: "c", IsFree: true, Resolution: "1080P", Size: "80 GB"}
	small := hdhiveCard{Title: "d", IsFree: true, Resolution: "1080P", Size: "2 GB"}
	if !hdhiveCardLess(big, small) {
		t.Error("同画质应大体积在前")
	}
}

// 解锁页 115 链接提取（含 115cdn 域名与全角截断）
func TestHdhive115Link(t *testing.T) {
	cases := []struct{ html, want string }{
		{`<input value="https://115.com/s/arvYE?password=a1b2#">`, "https://115.com/s/arvYE?password=a1b2"},
		{`<a href="https://115cdn.com/s/xyz123">链接</a>`, "https://115cdn.com/s/xyz123"},
		{`<p>链接：https://115.com/s/abc。</p>`, "https://115.com/s/abc"},
	}
	for _, tc := range cases {
		m := reHdhive115Link.FindString(tc.html)
		if m == "" {
			t.Errorf("未提取到链接: %s", tc.html)
			continue
		}
		if got := hdhiveTrimLink(m); got != tc.want {
			t.Errorf("提取 %q want %q", got, tc.want)
		}
	}
}

// TestHdhiveNormalizeTorrents 归一化：磁力兜底/来源截断/危险沉底/大小文本
func TestHdhiveNormalizeTorrents(t *testing.T) {
	danger := "dangerous"
	raw := []hdhiveTorrentRaw{
		{RawTitle: "Fake.Setup.exe", InfoHash: "aaaa", ThreatLevel: &danger, SizeBytes: 1200 * 1024 * 1024, Seeders: 19},
		{RawTitle: "Movie 2160p HDR GB", MagnetURL: "magnet:?xt=urn:btih:bbbb", InfoHash: "bbbb", Quality: "2160p", SizeBytes: 42 * 1024 * 1024 * 1024, Seeders: 240, Source: "knaben:The"},
		{RawTitle: "No magnet item", InfoHash: "cccc", Quality: "1080p", SizeBytes: 800 * 1024 * 1024, Seeders: 12, Source: "eztv"},
		{RawTitle: "", InfoHash: ""}, // 空条目应被丢弃
	}
	items := hdhiveNormalizeTorrents(raw)
	if len(items) != 3 {
		t.Fatalf("期望 3 条（空条目丢弃），实得 %d", len(items))
	}
	// 危险条目沉底
	if items[2].ThreatLevel != "dangerous" {
		t.Fatalf("危险条目应沉底，实得顺序: %s / %s / %s", items[0].RawTitle, items[1].RawTitle, items[2].RawTitle)
	}
	first := items[0]
	if first.MagnetURL != "magnet:?xt=urn:btih:bbbb" {
		t.Fatalf("保留原始磁力，实得 %s", first.MagnetURL)
	}
	if first.SizeText != "42.00 GB" {
		t.Fatalf("大小文本应为 42.00 GB，实得 %s", first.SizeText)
	}
	if first.Source != "knaben" {
		t.Fatalf("来源应截断为 knaben，实得 %s", first.Source)
	}
	third := items[1]
	if third.MagnetURL != "magnet:?xt=urn:btih:cccc" {
		t.Fatalf("缺磁力时应用 infoHash 兜底，实得 %s", third.MagnetURL)
	}
	if third.SizeText != "800 MB" || third.Source != "eztv" {
		t.Fatalf("800 MB/eztv 期望，实得 %s/%s", third.SizeText, third.Source)
	}
}
