package cover

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os/exec"
	"strings"
	"testing"
)

func TestEncode(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 80, G: 160, B: 220, A: 180})
		}
	}
	for _, extension := range []string{"jpg", "jpeg", "JPEG", "PNG", "webp", "WEBP"} {
		t.Run(extension, func(t *testing.T) {
			isJPEG := extension == "jpg" || strings.EqualFold(extension, "jpeg")
			var source bytes.Buffer
			if isJPEG {
				if err := jpeg.Encode(&source, img, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := png.Encode(&source, img); err != nil {
					t.Fatal(err)
				}
			}
			if extension == "webp" || extension == "WEBP" {
				args := []string{"-quiet", "-o", "-", "--", "-"}
				if extension == "WEBP" {
					args = append([]string{"-lossless"}, args...)
				}
				cmd := exec.Command("cwebp", args...)
				cmd.Stdin = bytes.NewReader(source.Bytes())
				encoded, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				source.Reset()
				source.Write(encoded)
			}
			webp, err := Encode(context.Background(), "cover."+extension, source.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			// Decode using the independent libwebp decoder, checking resize and transparency.
			cmd := exec.Command("dwebp", "-quiet", "-o", "-", "--", "-")
			cmd.Stdin = bytes.NewReader(webp)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Bounds().Dx() != 1600 || decoded.Bounds().Dy() != 800 {
				t.Fatal(decoded.Bounds())
			}
			_, _, _, alpha := decoded.At(100, 100).RGBA()
			if !isJPEG && alpha >= 65535 {
				t.Fatal("transparency lost")
			}
		})
	}
}

func TestRejectInvalidImages(t *testing.T) {
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 10, 10)))
	var tooWide bytes.Buffer
	_ = png.Encode(&tooWide, image.NewNRGBA(image.Rect(0, 0, 8193, 1)))
	var tooMany bytes.Buffer
	_ = png.Encode(&tooMany, image.NewNRGBA(image.Rect(0, 0, 4001, 4000)))
	webpData, err := Encode(context.Background(), "cover.png", pngData.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"cover.svg", []byte("<svg/>")}, {"cover.webp", pngData.Bytes()}, {"cover.jpeg", pngData.Bytes()},
		{"cover.jpg", pngData.Bytes()}, {"cover.png", []byte("invalid")}, {"cover.png", nil},
		{"cover.png", pngData.Bytes()[:30]}, {"cover.png", tooWide.Bytes()}, {"cover.png", tooMany.Bytes()},
		{"cover.png", make([]byte, MaxBytes+1)},
		{"cover.webp", []byte("invalid")}, {"cover.webp", nil},
		{"cover.webp", webpData[:len(webpData)-10]}, {"cover.png", webpData},
		{"cover.webp", make([]byte, MaxBytes+1)},
	} {
		if _, err := Encode(context.Background(), tc.name, tc.data); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %s (%d bytes): %v", tc.name, len(tc.data), err)
		}
	}
}
