package api

import (
	"encoding/json"
	"testing"
)

// 115 离线任务接口响应形态多变，解析器需全部兼容
func TestExtractTaskItems(t *testing.T) {
	cases := []string{
		`[{"name":"a.mkv","percent":45.5}]`,                                            // 顶层数组
		`{"state":true,"data":[{"name":"a.mkv"}]}`,                                      // data 数组
		`{"state":true,"info":[{"name":"a.mkv"}]}`,                                      // info 数组
		`{"state":true,"data":{"tasks":[{"name":"a.mkv"}],"count":1}}`,                  // data.tasks
		`{"state":true,"info":{"list":[{"name":"a.mkv"}]}}`,                             // info.list
		`{"state":true,"tasks":[{"name":"a.mkv"}]}`,                                     // 顶层 tasks
		`{"state":true,"data":{"info":[{"name":"a.mkv"}]}}`,                             // data.info
	}
	for i, c := range cases {
		items, ok := extractTaskItems([]byte(c))
		if !ok || len(items) != 1 || items[0]["name"] != "a.mkv" {
			t.Errorf("case %d failed: ok=%v items=%v", i, ok, items)
		}
	}
	if _, ok := extractTaskItems([]byte(`{"state":true}`)); ok {
		t.Error("empty response should not parse")
	}
	_ = json.Marshal
}
