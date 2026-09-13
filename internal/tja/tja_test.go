package tja

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedContract(t *testing.T) {
	data, err := os.ReadFile("../../contracts/validation.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Text, Audio, Code, Encoding string
		Raw                               []byte
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			b := []byte(c.Text)
			if c.Raw != nil {
				b = c.Raw
			}
			m, e := Parse(b, c.Encoding, c.Audio)
			code := ""
			if e != nil {
				code = e.Code
			}
			if code != c.Code {
				t.Fatalf("wanted %s, got %s: %v", c.Code, code, e)
			}
			if e == nil && (m.Title == "" || len(m.Difficulties) == 0) {
				t.Fatal("missing parsed metadata")
			}
		})
	}
}
func FuzzParse(f *testing.F) {
	f.Add([]byte("TITLE:Test\nBPM:120\nWAVE:music.ogg\nLEVEL:5\n#START\n1000,\n#END"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) <= MaxTJA {
			Parse(b, "utf-8", "music.ogg")
		}
	})
}
