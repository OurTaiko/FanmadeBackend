package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

// ffmpeg can conceal broken frame boundaries or a truncated last frame. Check
// framing first, then let the decoder validate compressed audio. This supports
// MPEG-1/2/2.5 Layer III CBR/VBR and common ID3v2, ID3v1 and APE metadata.
func checkMP3(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) >= 128 && string(b[len(b)-128:len(b)-125]) == "TAG" {
		b = b[:len(b)-128]
	}
	if len(b) >= 32 && string(b[len(b)-32:len(b)-24]) == "APETAGEX" {
		footer := b[len(b)-32:]
		size := int64(binary.LittleEndian.Uint32(footer[12:16]))
		if size < 32 || size > int64(len(b)) {
			return fmt.Errorf("invalid APE tag length")
		}
		b = b[:int64(len(b))-size]
		if len(b) >= 32 && string(b[len(b)-32:len(b)-24]) == "APETAGEX" {
			b = b[:len(b)-32]
		}
	}
	for bytes.HasPrefix(b, []byte("ID3")) {
		if len(b) < 10 || b[3] < 2 || b[3] > 4 || b[4] == 255 {
			return fmt.Errorf("invalid ID3 header")
		}
		size := 0
		for _, n := range b[6:10] {
			if n&128 != 0 {
				return fmt.Errorf("invalid ID3 size")
			}
			size = size<<7 | int(n)
		}
		size += 10
		if b[3] == 4 && b[5]&16 != 0 {
			size += 10 // ID3v2.4 footer is not included in the tag size.
		}
		if size > len(b) {
			return fmt.Errorf("truncated ID3 tag")
		}
		b = b[size:]
	}
	frames, stream := 0, byte(0)
	for len(b) > 0 {
		if len(b) < 4 || b[0] != 255 || b[1]&224 != 224 {
			return fmt.Errorf("invalid MP3 frame boundary")
		}
		version, layer := (b[1]>>3)&3, (b[1]>>1)&3
		bitrate, frequency := b[2]>>4, (b[2]>>2)&3
		if version == 1 || layer != 1 || bitrate == 0 || bitrate == 15 || frequency == 3 {
			return fmt.Errorf("unsupported MP3 frame header")
		}
		// Bitrate may vary, but the MPEG version and sampling rate may not.
		identity := version<<2 | frequency
		if frames > 0 && identity != stream {
			return fmt.Errorf("inconsistent MP3 stream")
		}
		stream = identity
		rates := [...]int{44100, 48000, 32000}
		rate := rates[frequency]
		bitrates := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
		factor := 144000
		if version != 3 {
			rate /= 2
			factor = 72000
			bitrates = [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
			if version == 0 {
				rate /= 2
			}
		}
		size := factor*bitrates[bitrate]/rate + int((b[2]>>1)&1)
		if size > len(b) {
			return fmt.Errorf("truncated MP3 frame")
		}
		b = b[size:]
		frames++
	}
	if frames < 2 {
		return fmt.Errorf("missing MP3 audio frames")
	}
	return nil
}
