package audio

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var crcTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

// CheckPages rejects truncated, corrupt, multiplexed and chained Ogg streams.
func CheckPages(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var serial, seq uint32
	pages := 0
	ended := false
	for {
		h := make([]byte, 27)
		_, e := io.ReadFull(f, h)
		if e == io.EOF {
			break
		}
		if e != nil {
			return fmt.Errorf("truncated Ogg header")
		}
		if ended || string(h[:4]) != "OggS" || h[4] != 0 || h[5]&^byte(7) != 0 {
			return fmt.Errorf("invalid Ogg page")
		}
		sn := binary.LittleEndian.Uint32(h[14:18])
		pn := binary.LittleEndian.Uint32(h[18:22])
		if pages == 0 {
			serial = sn
			if h[5]&2 == 0 || pn != 0 {
				return fmt.Errorf("missing Ogg beginning")
			}
		} else if sn != serial || pn != seq+1 || h[5]&2 != 0 {
			return fmt.Errorf("multiple or unordered Ogg streams")
		}
		seq = pn
		segments := make([]byte, int(h[26]))
		if _, e := io.ReadFull(f, segments); e != nil {
			return e
		}
		size := 0
		for _, n := range segments {
			size += int(n)
		}
		body := make([]byte, size)
		if _, e := io.ReadFull(f, body); e != nil {
			return e
		}
		expected := binary.LittleEndian.Uint32(h[22:26])
		clear(h[22:26])
		var crc uint32
		for _, part := range [][]byte{h, segments, body} {
			for _, b := range part {
				crc = (crc << 8) ^ crcTable[byte(crc>>24)^b]
			}
		}
		if crc != expected {
			return fmt.Errorf("Ogg checksum mismatch")
		}
		ended = h[5]&4 != 0
		pages++
	}
	if pages < 2 || !ended {
		return fmt.Errorf("incomplete Ogg stream")
	}
	return nil
}

// MediaType is also the upload extension allowlist. The actual bytes must pass Validate.
func MediaType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	default:
		return ""
	}
}

func Validate(ctx context.Context, path, originalName string) (float64, error) {
	media := MediaType(originalName)
	var checkErr error
	switch media {
	case "audio/ogg":
		checkErr = CheckPages(path)
	case "audio/mpeg":
		checkErr = checkMP3(path)
	default:
		return 0, fmt.Errorf("unsupported audio extension")
	}
	if checkErr != nil {
		return 0, checkErr
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-show_entries", "stream=codec_name,codec_type:stream_disposition=attached_pic:format=format_name,duration", "-of", "json", path).Output()
	if err != nil {
		return 0, fmt.Errorf("audio probe failed: %w", err)
	}
	var p struct {
		Streams []struct {
			CodecName   string `json:"codec_name"`
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		}
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		}
	}
	if json.Unmarshal(out, &p) != nil {
		return 0, fmt.Errorf("invalid audio probe response")
	}
	format, codec := "ogg", "vorbis"
	if media == "audio/mpeg" {
		format, codec = "mp3", "mp3"
	}
	if p.Format.FormatName != format {
		return 0, fmt.Errorf("audio container does not match extension")
	}
	tracks := 0
	for _, stream := range p.Streams {
		// MP3 album artwork is metadata, not an additional playable track.
		if media == "audio/mpeg" && stream.CodecType == "video" && stream.Disposition.AttachedPic == 1 {
			continue
		}
		if stream.CodecType != "audio" || stream.CodecName != codec {
			return 0, fmt.Errorf("unsupported audio stream")
		}
		tracks++
	}
	if tracks != 1 {
		return 0, fmt.Errorf("exactly one audio track is required")
	}
	duration, e := strconv.ParseFloat(p.Format.Duration, 64)
	if e != nil || math.IsInf(duration, 0) || math.IsNaN(duration) || duration <= 0 || duration > 1200 {
		return 0, fmt.Errorf("invalid duration")
	}
	if err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-threads", "1", "-protocol_whitelist", "file", "-i", path, "-map", "0:a:0", "-f", "null", "-").Run(); err != nil {
		return 0, fmt.Errorf("audio decode failed: %w", err)
	}
	return duration, nil
}
