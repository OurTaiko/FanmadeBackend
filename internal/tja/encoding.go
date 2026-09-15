package tja

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
)

// NormalizeUTF8 validates the declared input encoding and returns BOM-free UTF-8.
// Browsers detect other source encodings before upload; the API independently
// validates UTF-8 and normalizes explicitly declared Shift-JIS uploads as well.
func NormalizeUTF8(data []byte, encoding string) ([]byte, *Issue) {
	if len(data) == 0 || len(data) > MaxTJA {
		return nil, fail("FILE_SIZE_INVALID", "TJA 不能为空且不能超过 2 MiB", 0)
	}
	var err error
	switch encoding {
	case "utf-8":
		if !utf8.Valid(data) {
			return nil, fail("TJA_ENCODING_INVALID", "TJA 不是有效 UTF-8，请转存为 UTF-8 后重新上传", 0)
		}
	case "shift-jis":
		data, err = japanese.ShiftJIS.NewDecoder().Bytes(data)
	default:
		return nil, fail("TJA_ENCODING_INVALID", "不支持此文本编码", 0)
	}
	if err != nil || !utf8.Valid(data) || bytes.Contains(data, []byte("\uFFFD")) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fail("TJA_ENCODING_INVALID", "文本包含无法识别或损坏的字符", 0)
	}
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	if len(data) == 0 || len(data) > MaxTJA {
		return nil, fail("FILE_SIZE_INVALID", "转换后的 UTF-8 TJA 不能为空且不能超过 2 MiB", 0)
	}
	return data, nil
}
