package api

import "testing"

func TestDetectAVNumber(t *testing.T) {
	cases := []struct {
		dir, file, want string
	}{
		// 用户实测场景：目录名带番号，文件名带广告域名前缀
		{"START-622", "4k688.com@START-622.mp4", "START-622"},
		// 目录名没有番号，从文件名识别
		{"", "4k688.com@START-622.mp4", "START-622"},
		{"download", "MIDV-001.mp4", "MIDV-001"},
		// 常规格式
		{"MIDV-001", "movie.mp4", "MIDV-001"},
		{"SSIS-123", "SSIS-123-C.mp4", "SSIS-123"},
		{"abc-001", "x.mp4", "ABC-001"},         // 小写归一化
		{"MIDE-650@", "y.mkv", "MIDE-650"},      // 目录名带尾巴
		{"start100", "z.mp4", "START-100"},      // 无连字符
		// FC2 番号（数字 5-8 位，通用规则匹配不到）
		{"FC2-PPV-1234567", "a.mp4", "FC2-PPV-1234567"},
		{"", "FC2_998877.mp4", "FC2-PPV-998877"},
		// 误匹配排除：画质/编码标签不是番号
		{"1080P", "x265.mp4", ""},
		{"HD", "HEVC-123.mkv", ""},
		{"XX", "movie.mp4", ""},
		// 普通影视名不误判
		{"Up.2009.1080p.BluRay", "up.mkv", ""},
		{"西游记.1987", "ep01.mkv", ""},
	}
	for _, c := range cases {
		if got := detectAVNumber(c.dir, c.file); got != c.want {
			t.Errorf("detectAVNumber(%q, %q) = %q, want %q", c.dir, c.file, got, c.want)
		}
	}
}
