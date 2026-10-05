package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetadataPatch(t *testing.T) {
	original := metadataTranslations{map[string]string{"en": "Source", "ja": "原文"}, map[string]string{"en": ""}}
	o := metadataTranslations{map[string]string{"en": "Edited", "ja": "旧名", "zh": "中文"}, map[string]string{"ja": "副題"}}
	var p metadataPatch
	if err := json.Unmarshal([]byte(`{"title":null,"subtitle":"","titleTranslations":{"ja":"新名","zh":null},"subtitleTranslations":null}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.apply(&o, original) || o.Titles["en"] != "Source" || o.Titles["ja"] != "新名" || len(o.Titles) != 2 || o.Subtitles["en"] != "" || len(o.Subtitles) != 1 {
		t.Fatalf("patch/reset: %+v", o)
	}
	if !(metadataPatch{TitleTranslations: json.RawMessage(`{"en":"English","ko":"한국어"}`)}).apply(&o, original) || o.Titles["en"] != "English" || o.Titles["ko"] != "한국어" || o.Titles["ja"] != "新名" {
		t.Fatal("English must use the same dictionary")
	}
	for _, body := range []string{`{}`, `null`, `{"title":" "}`, `{"title":12}`, `{"subtitle":false}`, `{"titleTranslations":{"de":"Unsupported"}}`, `{"title":"One","titleTranslations":{"en":"Two"}}`, `{"titleTranslations":{"ja":""}}`, `{"subtitleTranslations":[]}`, `{"title":"a\u0000b"}`, `{"title":"` + strings.Repeat("x", 501) + `"}`} {
		var bad metadataPatch
		if err := json.Unmarshal([]byte(body), &bad); err != nil {
			t.Fatal(err)
		}
		if bad.apply(&metadataTranslations{}, original) {
			t.Fatalf("accepted %s", body)
		}
	}
}
