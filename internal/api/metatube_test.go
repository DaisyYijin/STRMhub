package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"strmhub/internal/model"
)

// metatubeTestSetup 独立内存库 + 假 metatube-server；返回请求计数指针
func metatubeTestSetup(t *testing.T, name, searchBody, detailBody string) (*httptest.Server, *int64) {
	t.Helper()
	if _, err := model.InitDB("file:" + name + "?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/movies/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchBody))
	})
	mux.HandleFunc("/v1/movies/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(detailBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		model.DB.Where("key = ?", "metatube").Delete(&model.Setting{})
		model.DB.Where("1 = 1").Delete(&model.AVMeta{})
	})
	saveSettingRow(t, "metatube", `{"url":"`+srv.URL+`","token":"","enabled":true}`)
	return srv, &hits
}

func saveSettingRow(t *testing.T, key, value string) {
	t.Helper()
	model.DB.Where("key = ?", key).Delete(&model.Setting{})
	if err := model.DB.Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
}

func TestMetatubeJSONStrings(t *testing.T) {
	// 数组形态
	if got := metatubeJSONStrings([]byte(`["a","b"]`)); len(got) != 2 || got[0] != "a" {
		t.Errorf("数组形态解析错误: %v", got)
	}
	// JSON 字符串形态（datatypes.JSON 兼容）
	if got := metatubeJSONStrings([]byte(`"[\"x\",\"y\"]"`)); len(got) != 2 || got[1] != "y" {
		t.Errorf("字符串形态解析错误: %v", got)
	}
	if got := metatubeJSONStrings(nil); got != nil {
		t.Errorf("空值应为 nil: %v", got)
	}
}

func TestMetatubeActorNames(t *testing.T) {
	got := metatubeActorNames([]byte(`[{"name":"三上悠亜","aliases":["SSIS"]},{"name":"天使もえ"}]`))
	if len(got) != 2 || got[0] != "三上悠亜" {
		t.Errorf("演员解析错误: %v", got)
	}
	// 字符串包裹形态
	got = metatubeActorNames([]byte(`"[{\"name\":\"河北彩花\"}]"`))
	if len(got) != 1 || got[0] != "河北彩花" {
		t.Errorf("字符串形态演员解析错误: %v", got)
	}
}

func TestMetatubeFetchCachedFlow(t *testing.T) {
	search := `[{"provider":"javbus","id":"abc123","title":"テスト作品","title_number":"ABC-123","cover_url":"","release_date":"2023-01-05","actors":[{"name":"演员A"}],"genres":["剧情"]}]`
	detail := `{"provider":"javbus","id":"abc123","title":"テスト作品 オフィスレディ編","original_title":"","description":"剧情简介","release_date":"2023-01-05","runtime":120,"director":"导演","publisher":"厂牌","cover_url":"http://example.com/cover.jpg","actors":[{"name":"演员A"},{"name":"演员B"}],"genres":["剧情","办公室"]}`
	_, hits := metatubeTestSetup(t, "mt_flow", search, detail)

	meta := metatubeFetchCached("ABC-123")
	if meta == nil || meta.Status != "ok" {
		t.Fatalf("首次刮削应命中: %+v", meta)
	}
	if meta.Title != "テスト作品 オフィスレディ編" {
		t.Errorf("标题取详情: %q", meta.Title)
	}
	if meta.Year != "2023" || meta.Runtime != 120 {
		t.Errorf("年份/时长解析错误: %q %d", meta.Year, meta.Runtime)
	}
	if actors := avMetaActors(meta); len(actors) != 2 {
		t.Errorf("演员应为 2 人: %v", actors)
	}
	// 第二次：命中缓存，不再请求服务器（返回同一条记录）
	firstID := meta.ID
	again := metatubeFetchCached("abc-123") // 大小写不同的同番号
	if again == nil || again.ID != firstID {
		t.Fatalf("缓存命中失败: %+v", again)
	}
	if n := atomic.LoadInt64(hits); n != 2 {
		t.Errorf("缓存生效时请求次数应为 2（search+detail），实际 %d", n)
	}
}

func TestMetatubeNotFoundTTL(t *testing.T) {
	_, hits := metatubeTestSetup(t, "mt_notfound", `[]`, `{}`)
	if meta := metatubeFetchCached("XYZ-999"); meta != nil {
		t.Fatalf("无结果应返回 nil: %+v", meta)
	}
	var row model.AVMeta
	if err := model.DB.Where("num = ?", "XYZ999").First(&row).Error; err != nil || row.Status != "not_found" {
		t.Fatalf("not_found 应落库: %v %+v", err, row)
	}
	// TTL 内不再请求
	metatubeFetchCached("XYZ-999")
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Errorf("not_found TTL 内不应重刮，请求次数 %d", n)
	}
	// 改旧时间 → 过期 → 重刮
	model.DB.Model(&row).Update("updated_at", time.Now().Add(-8*24*time.Hour))
	metatubeFetchCached("XYZ-999")
	if n := atomic.LoadInt64(hits); n != 2 {
		t.Errorf("not_found 过期后应重刮，请求次数 %d", n)
	}
}

func TestMetatubeDisabled(t *testing.T) {
	_, hits := metatubeTestSetup(t, "mt_disabled", `[]`, `{}`)
	saveSettingRow(t, "metatube", `{"url":"http://x","enabled":false}`)
	if meta := metatubeFetchCached("ABC-001"); meta != nil {
		t.Fatalf("未启用应返回 nil: %+v", meta)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Errorf("未启用不应发请求，实际 %d", n)
	}
}

func TestWriteAVMetaFiles(t *testing.T) {
	local := t.TempDir()
	saveSettingRow(t, "full", `{"local_path":"`+strings.ReplaceAll(local, "\\", "\\\\")+`"}`)
	meta := &model.AVMeta{
		Num: "ABC123", Status: "ok", Title: "タイトル/危険文字", Year: "2022",
		ReleaseDate: "2022-03-04", Plot: "简介 <转义> & 测试", Publisher: "PRESTIGE",
		ActorsJSON: `["演员A","演员B"]`, GenresJSON: `["剧情"]`,
	}
	avDir := "无码/ABC-123"
	writeAVMetaFiles(avDir, meta, "ABC-123")

	nfo, err := os.ReadFile(filepath.Join(local, "无码", "ABC-123", "movie.nfo"))
	if err != nil {
		t.Fatalf("movie.nfo 未写入: %v", err)
	}
	s := string(nfo)
	for _, want := range []string{"<title>ABC-123 タイトル/危険文字</title>", "<year>2022</year>", "<premiered>2022-03-04</premiered>", "简介 &lt;转义&gt; &amp; 测试", "<name>演员A</name>"} {
		if !strings.Contains(s, want) {
			t.Errorf("NFO 缺少 %q\n%s", want, s)
		}
	}
	// cover 为空 → 不应产生 poster.jpg 但 NFO 照常
	if _, err := os.Stat(filepath.Join(local, "无码", "ABC-123", "poster.jpg")); !os.IsNotExist(err) {
		t.Errorf("cover 为空不应写 poster.jpg")
	}
}

func TestRecordAVMediaDedupe(t *testing.T) {
	metatubeTestSetup(t, "mt_bare", `[]`, `[]`) // 仅借 DB 初始化
	meta := &model.AVMeta{Num: "DDD001", Status: "ok", Title: "标题一", Year: "2024", CoverURL: "http://c/1.jpg", Plot: "简介"}
	recordAVMedia(meta, "DDD-001", "有码", "有码/DDD-001")
	recordAVMedia(meta, "DDD-001", "有码", "有码/DDD-001") // 重复入库 → 更新而非新增
	var count int64
	model.DB.Model(&model.MediaLibrary{}).Where("media_type = ?", "av").Count(&count)
	if count != 1 {
		t.Fatalf("AV 记录应去重为 1 条，实际 %d", count)
	}
	var rec model.MediaLibrary
	model.DB.Where("media_type = ? AND original_title = ?", "av", "DDD-001").First(&rec)
	if rec.PosterPath != "/av:http://c/1.jpg" {
		t.Errorf("PosterPath 应带 /av: 前缀: %q", rec.PosterPath)
	}
	// 无元数据 → 标题回退番号
	recordAVMedia(nil, "DDD-002", "有码", "有码/DDD-002")
	rec = model.MediaLibrary{} // GORM First 不会清零 dest，残留主键会附加 id 条件
	model.DB.Where("media_type = ? AND original_title = ?", "av", "DDD-002").First(&rec)
	if rec.Title != "DDD-002" || rec.PosterPath != "" {
		t.Errorf("无元数据应回退番号: %+v", rec)
	}
}

func TestRenameAVMetaVars(t *testing.T) {
	metatubeTestSetup(t, "mt_rename", `[]`, `[]`)
	model.DB.Create(&model.AVMeta{
		Num: "EEE002", Status: "ok", Title: "タイトル/危険", Year: "2021",
		ActorsJSON: `["演员A","演员B"]`,
	})
	media := &TmdbMedia{Title: "EEE-002", MediaType: "av"}
	ctx := buildRenameContext(media, &ParsedName{Title: "EEE-002"}, "EEE-002.mp4")
	if got := ctx.ApplyTemplate("{num} {av_title} ({av_year})"); got != "EEE-002 タイトル 危険 (2021)" {
		t.Errorf("模板输出错误: %q", got)
	}
	if got := ctx.ApplyTemplate("{actor}"); got != "演员A" {
		t.Errorf("{actor} 应为第一主演: %q", got)
	}
	if got := ctx.ApplyTemplate("{actors}"); got != "演员A、演员B" {
		t.Errorf("{actors} 错误: %q", got)
	}
	// 无缓存 → 变量为空，块语法整块丢弃
	media2 := &TmdbMedia{Title: "FFF-003", MediaType: "av"}
	ctx2 := buildRenameContext(media2, &ParsedName{}, "x.mp4")
	if got := ctx2.ApplyTemplate("{num}< {av_title}>{ext}"); got != "FFF-003.mp4" {
		t.Errorf("无刮削结果应退回番号: %q", got)
	}
}
