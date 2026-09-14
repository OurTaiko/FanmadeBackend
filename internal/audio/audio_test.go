package audio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidateAudio(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is required")
		}
	}
	for _, name := range []string{"cbr.mp3", "raw.mp3", "vbr.mp3", "cover.mp3", "vorbis.ogg"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			// The upload staging file and game cache do not preserve extensions.
			path := filepath.Join(t.TempDir(), "audio.ogg")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			duration, err := Validate(context.Background(), path, name)
			if err != nil || duration < .4 || duration > 1 {
				t.Fatalf("duration=%f err=%v", duration, err)
			}
			wrong := "fake.ogg"
			if filepath.Ext(name) == ".ogg" {
				wrong = "fake.mp3"
			}
			if _, err := Validate(context.Background(), path, wrong); err == nil {
				t.Fatal("accepted mismatched extension")
			}
			if err := os.WriteFile(path, data[:len(data)-11], 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Validate(context.Background(), path, name); err == nil {
				t.Fatal("accepted truncated audio")
			}
		})
	}
}

func TestRejectFakeMP3(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("ID3fake"), {'I', 'D', '3', 4, 0, 0, 127, 127, 127, 127}, {255, 251, 144, 0}} {
		path := filepath.Join(t.TempDir(), "audio")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkMP3(path); err == nil {
			t.Fatal("accepted malformed MP3")
		}
	}
}
