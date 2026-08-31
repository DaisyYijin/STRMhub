package api

import (
	"os"
	"math/big"
	"testing"
)

// TestGyPowLoopMath 验证 RSW 求解循环（t 次平方取模）等价于 x^(2^t) mod N
func TestGyPowLoopMath(t *testing.T) {
	cases := []struct {
		nHex, xHex string
		t          int
	}{
		{"61", "2", 5},
		{"9a7f3", "1c", 17},
		{"fffffffffffffffffffffffffffffffe", "abcdef0123456789", 33},
	}
	for _, c := range cases {
		n, _ := new(big.Int).SetString(c.nHex, 16)
		x, _ := new(big.Int).SetString(c.xHex, 16)
		// 期望值：x^(2^t) mod N（直接构造 2^t 次幂）
		exp := new(big.Int).Lsh(big.NewInt(1), uint(c.t))
		want := new(big.Int).Exp(x, exp, n)
		// 迭代实现
		y := new(big.Int).Set(x)
		for i := 0; i < c.t; i++ {
			y.Mul(y, y)
			y.Mod(y, n)
		}
		if y.Cmp(want) != 0 {
			t.Errorf("RSW loop mismatch: N=%s x=%s t=%d got %s want %s", c.nHex, c.xHex, c.t, y.Text(16), want.Text(16))
		}
	}
}

func TestGyDetectPan(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://115.com/s/abc123", "115"},
		{"https://115cdn.com/s/-Ab_9", "115"},
		{"https://pan.quark.cn/s/1a2b3c", "quark"},
		{"https://pan.baidu.com/s/1xyz?pwd=ab12", "baidu"},
		{"magnet:?xt=urn:btih:0123456789abcdef", "magnet"},
		{"https://www.xn--wcv59z.com/search", ""},
		{"https://example.com/s/abc", ""},
	}
	for _, c := range cases {
		if got := gyDetectPan(c.in); got != c.want {
			t.Errorf("gyDetectPan(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGyExtractLinks(t *testing.T) {
	html := `<html><script>var ignore="https://pan.quark.cn/s/shouldnotappear";</script>
	<body><a href="https://pan.baidu.com/s/1abcxyz">链接</a> 提取码：ab12
	<p>115: https://115.com/s/-swVqg_9H</p></body></html>`
	links := gyExtractLinks(html)
	var baidu, g115, quark bool
	for _, l := range links {
		switch l.URL {
		case "https://pan.baidu.com/s/1abcxyz":
			baidu = true
			if l.Code != "ab12" {
				t.Errorf("baidu code = %q, want ab12", l.Code)
			}
		case "https://115.com/s/-swVqg_9H":
			g115 = true
		case "https://pan.quark.cn/s/shouldnotappear":
			quark = true
		}
	}
	if !baidu || !g115 {
		t.Errorf("missing links: baidu=%v 115=%v", baidu, g115)
	}
	if quark {
		t.Errorf("script 内链接不应被提取")
	}
}

// TestGyExtractAnchorsSample 用登录态搜索页的真实 SSR HTML 验证条目解析
func TestGyExtractAnchorsSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/gy_search.html")
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	items := gyExtractAnchors(string(raw))
	if len(items) == 0 {
		t.Fatalf("真实样本解析出 0 个条目")
	}
	var found2018 bool
	for _, it := range items {
		if it["path"] == "/mv/GB3j" {
			found2018 = true
			if it["title"] != "无名之辈" || it["year"] != "2018" {
				t.Errorf("GB3j 解析异常: %v", it)
			}
		}
	}
	if !found2018 {
		t.Errorf("未解析到 /mv/GB3j（无名之辈 2018）")
	}
}
