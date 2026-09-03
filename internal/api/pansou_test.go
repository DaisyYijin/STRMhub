package api

import (
	"encoding/json"
	"testing"
)

// TestPansouNormalize 归一化：去重/类型排序（115 最前未知沉底）/时间倒序/动作分类
func TestPansouNormalize(t *testing.T) {
	resp := `{"code":0,"message":"success","data":{"total":5,"merged_by_type":{
		"quark":[{"url":"https://pan.quark.cn/s/a","password":"","note":"A 夸克","datetime":"2026-01-31T02:46:03+08:00"},
		         {"url":"https://pan.quark.cn/s/b","password":"x","note":"B 夸克","datetime":"2026-02-01T00:00:00+08:00"}],
		"baidu":[{"url":"https://pan.baidu.com/s/c","password":"8kfj","note":"C 百度","datetime":"2026-03-01T00:00:00+08:00"}],
		"moby":[{"url":"magnet:?xt=urn:btih:abc","password":"","note":"磁力","datetime":"2026-01-01T00:00:00+08:00"}]
	}}}`
	var out struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || out.Code != 0 {
		t.Fatalf("样例响应非法: %v", err)
	}
	var data struct {
		Total        int                          `json:"total"`
		MergedByType map[string][]json.RawMessage `json:"merged_by_type"`
	}
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	items := make([]PansouItem, 0, data.Total)
	seen := map[string]bool{}
	for ctype, arr := range data.MergedByType {
		for _, raw := range arr {
			var it struct {
				URL      string `json:"url"`
				Password string `json:"password"`
				Note     string `json:"note"`
				Datetime string `json:"datetime"`
			}
			if json.Unmarshal(raw, &it) != nil || it.URL == "" || seen[it.URL] {
				continue
			}
			seen[it.URL] = true
			items = append(items, PansouItem{CloudType: ctype, URL: it.URL, Password: it.Password, Note: it.Note, Datetime: it.Datetime, Action: pansouActionFor(it.URL)})
		}
	}
	// 与 PansouSearch 相同的排序
	sortItems := func(items []PansouItem) {
		for i := 0; i < len(items); i++ {
			for j := i + 1; j < len(items); j++ {
				ri, rj := pansouTypeRank[items[i].CloudType], pansouTypeRank[items[j].CloudType]
				if ri == 0 {
					ri = 9
				}
				if rj == 0 {
					rj = 9
				}
				if ri > rj || (ri == rj && items[i].Datetime < items[j].Datetime) {
					items[i], items[j] = items[j], items[i]
				}
			}
		}
	}
	sortItems(items)

	if len(items) != 4 {
		t.Fatalf("期望 4 条，实得 %d", len(items))
	}
	if items[0].CloudType != "quark" || items[0].URL != "https://pan.quark.cn/s/b" {
		t.Fatalf("quark 应在最前且新时间在前，实得 %+v", items[0])
	}
	if items[3].CloudType != "moby" || items[3].Action != "offline" {
		t.Fatalf("磁力应沉底且动作为 offline，实得 %+v", items[3])
	}
	if act := pansouActionFor("https://115cdn.com/s/xyz?password=ab"); act != "transfer" {
		t.Fatalf("115 分享应为 transfer，实得 %s", act)
	}
	if act := pansouActionFor("https://www.alipan.com/s/xyz"); act != "open" {
		t.Fatalf("阿里链接应为 open，实得 %s", act)
	}
}
