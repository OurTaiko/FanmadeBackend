package tja

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/japanese"
)

func TestNormalizeUTF8(t *testing.T) {
	want := []byte("TITLE:初音ミク\r\nWAVE:音楽.mp3\r\n")
	sjis, err := japanese.ShiftJIS.NewEncoder().Bytes(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		encoding string
		data     []byte
	}{
		{"utf-8", want}, {"utf-8", append([]byte("\uFEFF"), want...)}, {"shift-jis", sjis},
	} {
		got, issue := NormalizeUTF8(tc.data, tc.encoding)
		if issue != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v, %q", tc.encoding, issue, got)
		}
	}
	for _, tc := range []struct {
		encoding string
		data     []byte
		code     string
	}{
		{"utf-8", []byte{0xff}, "TJA_ENCODING_INVALID"},
		{"utf-8", []byte("\uFFFD"), "TJA_ENCODING_INVALID"},
		{"utf-8", []byte("TITLE:a\x00"), "TJA_ENCODING_INVALID"},
		{"shift-jis", []byte{0x82}, "TJA_ENCODING_INVALID"},
		{"unknown", want, "TJA_ENCODING_INVALID"},
		{"utf-8", []byte("\uFEFF"), "FILE_SIZE_INVALID"},
		{"shift-jis", bytes.Repeat([]byte{0x82, 0xa0}, MaxTJA/3+1), "FILE_SIZE_INVALID"},
	} {
		_, issue := NormalizeUTF8(tc.data, tc.encoding)
		if issue == nil || issue.Code != tc.code {
			t.Fatalf("%s: %v", tc.encoding, issue)
		}
	}
}
