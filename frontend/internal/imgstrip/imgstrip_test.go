package imgstrip

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"
)

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 5), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

func TestProcess_JPEGStripOnly_NoResize(t *testing.T) {
	in := makeJPEG(t, 200, 100)
	out, ext, err := Process(in, 0)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	if ext != "jpg" {
		t.Fatalf("ext = %q; want jpg", ext)
	}
	if http.DetectContentType(out) != "image/jpeg" {
		t.Fatalf("output is not jpeg: %q", http.DetectContentType(out))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if cfg.Width != 200 || cfg.Height != 100 {
		t.Fatalf("dims = %dx%d; want 200x100", cfg.Width, cfg.Height)
	}
}

func TestProcess_JPEGDownsize_LongEdgeCapped(t *testing.T) {
	in := makeJPEG(t, 4000, 3000)
	out, _, err := Process(in, 1920)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if cfg.Width != 1920 {
		t.Fatalf("width = %d; want 1920", cfg.Width)
	}

	if cfg.Height < 1439 || cfg.Height > 1440 {
		t.Fatalf("height = %d; want ~1440", cfg.Height)
	}
	if len(out) >= len(in) {
		t.Fatalf("downsized size %d not smaller than input %d", len(out), len(in))
	}
}

func TestProcess_JPEGSmaller_NoUpscale(t *testing.T) {
	in := makeJPEG(t, 800, 600)
	out, _, err := Process(in, 1920)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if cfg.Width != 800 || cfg.Height != 600 {
		t.Fatalf("dims = %dx%d; want 800x600 (no upscale)", cfg.Width, cfg.Height)
	}
}

func TestProcess_PNGStripOnly_PreservesPNG(t *testing.T) {
	in := makePNG(t, 100, 100)
	out, ext, err := Process(in, 0)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	if ext != "png" {
		t.Fatalf("ext = %q; want png", ext)
	}
	if http.DetectContentType(out) != "image/png" {
		t.Fatalf("output is not png: %q", http.DetectContentType(out))
	}
}

func TestProcess_PortraitDownsize(t *testing.T) {
	in := makeJPEG(t, 1000, 4000)
	out, _, err := Process(in, 1920)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if cfg.Height != 1920 {
		t.Fatalf("height = %d; want 1920", cfg.Height)
	}

	if cfg.Width != 480 {
		t.Fatalf("width = %d; want 480", cfg.Width)
	}
}

func TestProcess_GIFBypassed(t *testing.T) {

	gif := []byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61,
		0x01, 0x00, 0x01, 0x00,
		0x00, 0x00, 0x00, 0x3b,
	}
	out, ext, err := Process(gif, 1920)
	if !errors.Is(err, ErrUnsupportedMIME) {
		t.Fatalf("err = %v; want ErrUnsupportedMIME", err)
	}
	if ext != "" {
		t.Fatalf("ext = %q; want empty on bypass", ext)
	}
	if &out[0] != &gif[0] {
		t.Fatalf("expected input slice returned untouched")
	}
}

func TestProcess_NonImageBypassed(t *testing.T) {
	in := []byte("plain text not an image at all")
	out, _, err := Process(in, 1920)
	if !errors.Is(err, ErrUnsupportedMIME) {
		t.Fatalf("err = %v; want ErrUnsupportedMIME", err)
	}
	if &out[0] != &in[0] {
		t.Fatalf("expected input slice returned untouched")
	}
}

func TestProcess_EmptyBypassed(t *testing.T) {
	out, _, err := Process(nil, 1920)
	if !errors.Is(err, ErrUnsupportedMIME) {
		t.Fatalf("err = %v; want ErrUnsupportedMIME", err)
	}
	if out != nil {
		t.Fatalf("expected nil output for nil input")
	}
}

func TestProcess_CorruptJPEG(t *testing.T) {

	in := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xde, 0xad, 0xbe, 0xef}
	out, ext, err := Process(in, 1920)
	if err == nil {
		t.Fatalf("expected decode error")
	}
	if errors.Is(err, ErrUnsupportedMIME) {
		t.Fatalf("err = ErrUnsupportedMIME; want decode err for sniffed-jpeg input")
	}
	if ext != "" {
		t.Fatalf("ext = %q; want empty on decode failure", ext)
	}
	if &out[0] != &in[0] {
		t.Fatalf("expected input slice returned on failure (so caller can ship raw)")
	}
}

func TestProcess_StripsEXIF(t *testing.T) {

	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for x := 0; x < 32; x++ {
		src.Set(x, x, color.RGBA{R: 0xff, A: 0xff})
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	srcBytes := buf.Bytes()
	exifBlob := []byte{
		0xff, 0xe1, 0x00, 0x0c,
		'E', 'x', 'i', 'f', 0x00, 0x00,
		0xaa, 0xbb,
	}
	withExif := append([]byte{}, srcBytes[:2]...)
	withExif = append(withExif, exifBlob...)
	withExif = append(withExif, srcBytes[2:]...)
	if !bytes.Contains(withExif, []byte("Exif\x00\x00")) {
		t.Fatalf("setup: input should contain EXIF magic")
	}

	out, _, err := Process(withExif, 0)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	if bytes.Contains(out, []byte("Exif\x00\x00")) {
		t.Fatalf("EXIF magic survived re-encode")
	}
}

func exifOrientationBlob(n byte) []byte {
	tiff := []byte{
		'I', 'I', 0x2a, 0x00,
		0x08, 0x00, 0x00, 0x00,
		0x01, 0x00,
		0x12, 0x01,
		0x03, 0x00,
		0x01, 0x00, 0x00, 0x00,
		n, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(payload) + 2
	blob := []byte{0xff, 0xe1, byte(segLen >> 8), byte(segLen)}
	return append(blob, payload...)
}

func spliceEXIF(jpegBytes, blob []byte) []byte {
	out := append([]byte{}, jpegBytes[:2]...)
	out = append(out, blob...)
	return append(out, jpegBytes[2:]...)
}

func TestOrientationOf(t *testing.T) {
	base := makeJPEG(t, 16, 16)
	for n := byte(1); n <= 8; n++ {
		got := orientationOf(spliceEXIF(base, exifOrientationBlob(n)))
		if got != int(n) {
			t.Errorf("orientationOf(tag=%d) = %d; want %d", n, got, n)
		}
	}
	if got := orientationOf(base); got != 1 {
		t.Errorf("orientationOf(no EXIF) = %d; want 1", got)
	}
	if got := orientationOf([]byte("not a jpeg at all")); got != 1 {
		t.Errorf("orientationOf(non-jpeg) = %d; want 1", got)
	}
}

func TestApplyOrientation(t *testing.T) {

	red := color.RGBA{R: 0xff, A: 0xff}
	green := color.RGBA{G: 0xff, A: 0xff}
	blue := color.RGBA{B: 0xff, A: 0xff}
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, red)
	src.Set(2, 0, green)
	src.Set(0, 1, blue)
	src.Set(2, 1, white)

	rgba := func(im image.Image, x, y int) color.RGBA {
		r, g, b, a := im.At(x, y).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}

	type check struct {
		x, y int
		want color.RGBA
	}
	cases := []struct {
		o      int
		w, h   int
		checks []check
	}{

		{6, 2, 3, []check{{0, 0, blue}, {1, 0, red}, {0, 2, white}, {1, 2, green}}},

		{8, 2, 3, []check{{0, 0, green}, {1, 0, white}, {0, 2, red}, {1, 2, blue}}},

		{3, 3, 2, []check{{2, 1, red}, {0, 1, green}, {2, 0, blue}, {0, 0, white}}},
	}
	for _, c := range cases {
		out := applyOrientation(src, c.o)
		b := out.Bounds()
		if b.Dx() != c.w || b.Dy() != c.h {
			t.Errorf("o=%d dims = %dx%d; want %dx%d", c.o, b.Dx(), b.Dy(), c.w, c.h)
			continue
		}
		for _, ck := range c.checks {
			if got := rgba(out, ck.x, ck.y); got != ck.want {
				t.Errorf("o=%d pixel(%d,%d) = %v; want %v", c.o, ck.x, ck.y, got, ck.want)
			}
		}
	}

	if applyOrientation(src, 1) != image.Image(src) {
		t.Errorf("o=1 should return the input untouched")
	}
}

func TestProcess_BakesOrientation(t *testing.T) {

	in := spliceEXIF(makeJPEG(t, 200, 100), exifOrientationBlob(6))
	out, _, err := Process(in, 0)
	if err != nil {
		t.Fatalf("Process err: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if cfg.Width != 100 || cfg.Height != 200 {
		t.Fatalf("output %dx%d; want 100x200 (orientation baked in)", cfg.Width, cfg.Height)
	}
	if bytes.Contains(out, []byte("Exif\x00\x00")) {
		t.Fatalf("EXIF magic survived re-encode")
	}
}
