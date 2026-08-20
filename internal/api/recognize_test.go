package api

import (
	"strings"
	"testing"
)

func TestSplitCJKLatin(t *testing.T) {
	cases := []struct {
		title, cjk, latin string
	}{
		// 用户实测场景：中文名 + 方括号标注 + 英文名
		{"骗不了人的男人[国日多音轨+中文字幕] Softie Conman", "骗不了人的男人", "Softie Conman"},
		// 纯中文名 + 英文名，空格分隔
		{"骗不了人的男人 Softie Conman", "骗不了人的男人", "Softie Conman"},
		// 纯中文 → latin 为空（不触发拆分）
		{"骗不了人的男人", "骗不了人的男人", ""},
		// 纯英文 → cjk 为空
		{"The Shawshank Redemption", "", "The Shawshank Redemption"},
		// 中文段取最长；空格会把相邻中文段连成一个整体（无法区分时宁可整段搜索）
		{"高清影视之家发布 骗不了人的男人 Softie Conman", "高清影视之家发布 骗不了人的男人", "Softie Conman"},
	}
	for _, c := range cases {
		cjk, latin := splitCJKLatin(c.title)
		if cjk != c.cjk || latin != c.latin {
			t.Errorf("splitCJKLatin(%q) = (%q, %q), want (%q, %q)", c.title, cjk, latin, c.cjk, c.latin)
		}
	}
}

func TestStripReleaseAds(t *testing.T) {
	cases := []struct{ in, want string }{
		// 全角括号广告块 + 域名（点号保留，由 parseFileName 统一替换为空格）
		{"【高清影视之家发布 www.SSDSSE.com】骗不了人的男人.Softie.Conman.2022.1080p", "骗不了人的男人.Softie.Conman.2022.1080p"},
		// 裸域名
		{"骗不了人的男人 www.4k688.com.mkv", "骗不了人的男人 .mkv"},
		// 干净名字不受影响（Softie.Conman 的 .Conman 不是 TLD）
		{"Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265", "Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265"},
	}
	for _, c := range cases {
		if got := stripReleaseAds(c.in); got != c.want {
			t.Errorf("stripReleaseAds(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFileNameAdPrefix(t *testing.T) {
	// 广告块剥离后，标题应从真实片名开始提取
	p := parseFileName("【高清影视之家发布 www.SSDSSE.com】骗不了人的男人.Softie.Conman.2022.1080p.MyTVSuper.WEB-DL.H265.AAC-QuickIO.mkv")
	for _, bad := range []string{"高清影视之家", "SSDSSE", "www"} {
		if strings.Contains(p.Title, bad) {
			t.Errorf("标题仍含广告 %q: %q", bad, p.Title)
		}
	}
	if p.Year != "2022" {
		t.Errorf("年份解析错误: %q", p.Year)
	}
	if p.Resolution != "1080P" {
		t.Errorf("分辨率解析错误: %q", p.Resolution)
	}
}
