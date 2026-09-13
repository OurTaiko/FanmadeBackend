package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetadataPatch(t *testing.T) {
	old := "English"
	o := metadataOverrides{Title: &old, Titles: map[string]string{"ja": "旧名", "zh": "中文"}, Subtitles: map[string]string{"ja": "副題"}}
	var p metadataPatch
	if err := json.Unmarshal([]byte(`{"title":null,"subtitle":"","titleTranslations":{"ja":"新名","zh":null},"subtitleTranslations":null}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.apply(&o) || o.Title != nil || o.Subtitle == nil || *o.Subtitle != "" || o.Titles["ja"] != "新名" || len(o.Titles) != 1 || len(o.Subtitles) != 0 {
		t.Fatalf("patch/reset: %+v", o)
	}
	for _, body := range []string{`{}`, `null`, `{"title":" "}`, `{"title":12}`, `{"subtitle":false}`, `{"titleTranslations":{"en":"wrong field"}}`, `{"titleTranslations":{"ja":""}}`, `{"subtitleTranslations":[]}`, `{"title":"a\u0000b"}`, `{"title":"` + strings.Repeat("x", 501) + `"}`} {
		var bad metadataPatch
		if err := json.Unmarshal([]byte(body), &bad); err != nil {
			t.Fatal(err)
		}
		if bad.apply(&metadataOverrides{}) {
			t.Fatalf("accepted %s", body)
		}
	}
}
