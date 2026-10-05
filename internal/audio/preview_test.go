package audio

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestPreviewMP3AndOgg(t *testing.T) {
	for _, name := range []string{"cbr.mp3", "vorbis.ogg"} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "preview.ogg")
			if err := Preview(context.Background(), filepath.Join("testdata", name), dst, 0.1, 0.35, 0.5); err != nil {
				t.Fatal(err)
			}
			duration, err := Validate(context.Background(), dst, "preview.ogg")
			if err != nil || math.Abs(duration-0.25) > 0.03 {
				t.Fatalf("duration=%v err=%v", duration, err)
			}
		})
	}
	dst := filepath.Join(t.TempDir(), "preview.ogg")
	if err := Preview(context.Background(), "testdata/vorbis.ogg", dst, 0.1, 15.1, 0.5); err != nil {
		t.Fatal(err)
	}
	duration, err := Validate(context.Background(), dst, "preview.ogg")
	if err != nil || math.Abs(duration-0.4) > 0.03 {
		t.Fatalf("EOF duration=%v err=%v", duration, err)
	}
}
func TestPreviewInvalidRange(t *testing.T) {
	for _, pair := range [][2]float64{{-1, 5}, {10, 15}, {5, 5}, {5, 4}, {0, math.Inf(1)}, {math.NaN(), 15}, {0, math.NaN()}, {0, 1216}} {
		if ValidPreviewRange(pair[0], pair[1], 10) {
			t.Fatalf("accepted %v", pair)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Preview(ctx, "testdata/vorbis.ogg", filepath.Join(t.TempDir(), "preview.ogg"), 0, 0.2, 0.5); err == nil {
		t.Fatal("canceled generation succeeded")
	}
}
