package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strmhub/internal/cd2"
)

func TestCd2GrpcTarget(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://1.2.3.4:19798", "1.2.3.4:19798"},
		{"https://nas.local:9900", "nas.local:9900"},
		{"nas.local", "nas.local:19798"},
		{"1.2.3.4:9900", "1.2.3.4:9900"},
	}
	for _, c := range cases {
		got, err := cd2GrpcTarget(c.in)
		if err != nil || got != c.want {
			t.Errorf("cd2GrpcTarget(%q) = %q,%v want %q", c.in, got, err, c.want)
		}
	}
	if _, err := cd2GrpcTarget("  "); err == nil {
		t.Error("empty endpoint should error")
	}
}

func TestCd2BuildProxyURL(t *testing.T) {
	got := cd2BuildProxyURL("http://192.168.1.5:19798", "/static/{SCHEME}/{HOST}/{PREVIEW}/a/b.mkv?token=x")
	want := "http://192.168.1.5:19798/static/http/192.168.1.5:19798/false/a/b.mkv?token=x"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// 无前缀地址默认 http + 19798
	got = cd2BuildProxyURL("nas.local", "static/{SCHEME}/x")
	want = "http://nas.local:19798/static/http/x"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCd2PickTarget(t *testing.T) {
	const ep = "http://n:19798"
	// 无 UA 要求的直链优先
	got := cd2PickTarget(ep, true, &cd2.URLInfo{DirectURL: "https://dl/x.mkv", ProxyPath: "/p"})
	if got != "https://dl/x.mkv" {
		t.Errorf("direct preferred: %q", got)
	}
	// 有 UA 要求 → 中转（播放器无法自定义 UA）
	got = cd2PickTarget(ep, true, &cd2.URLInfo{DirectURL: "https://dl/x.mkv", UserAgent: "android", ProxyPath: "/static/{SCHEME}/{HOST}/{PREVIEW}/x"})
	if !strings.Contains(got, "http://n:19798/static/http/n:19798/false/x") {
		t.Errorf("UA-bound direct should fall back to proxy: %q", got)
	}
	// 未开直链偏好 → 中转
	got = cd2PickTarget(ep, false, &cd2.URLInfo{DirectURL: "https://dl/x.mkv", ProxyPath: "/p"})
	if got != "http://n:19798/p" {
		t.Errorf("no prefer: %q", got)
	}
	// 什么都没有 → 空
	if got := cd2PickTarget(ep, true, &cd2.URLInfo{}); got != "" {
		t.Errorf("empty info: %q", got)
	}
}

func TestWriteStrmCd2(t *testing.T) {
	dir := t.TempDir()
	full := "/115网盘/媒体/剧/E01.mkv"
	written, err := writeStrmCd2(dir, "http://p:6086", "file_id", true, false, "剧/第一季", "E01.mkv", full)
	if err != nil || !written {
		t.Fatalf("write: %v written=%v", err, written)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "剧", "第一季", "E01.mkv.strm"))
	body := string(b)
	if !strings.HasPrefix(body, "http://p:6086/cd2/") {
		t.Fatalf("bad strm content: %q", body)
	}
	seg := strings.SplitN(strings.TrimPrefix(body, "http://p:6086/cd2/"), ".", 2)[0]
	if seg == "" || strings.ContainsAny(seg, "=+/") {
		t.Errorf("id 应为无填充 base64url: %q", seg)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil || string(decoded) != full {
		t.Errorf("id 解码回环失败: %q err=%v", decoded, err)
	}
	if !strings.HasSuffix(body, ".mkv") {
		t.Errorf("keepExt 应保留扩展名: %q", body)
	}
	// skipExist 幂等
	written2, err := writeStrmCd2(dir, "http://p:6086", "file_id", true, true, "剧/第一季", "E01.mkv", full)
	if err != nil || written2 {
		t.Errorf("skipExist should skip: %v written=%v", err, written2)
	}
}
