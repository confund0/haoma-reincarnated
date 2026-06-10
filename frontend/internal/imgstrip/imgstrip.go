package imgstrip

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const DefaultMaxLongEdge = 1920

const jpegQuality = 90

var ErrUnsupportedMIME = errors.New("imgstrip: unsupported MIME")

func Process(in []byte, maxLongEdge int) ([]byte, string, error) {
	if len(in) == 0 {
		return in, "", ErrUnsupportedMIME
	}

	mime := http.DetectContentType(in)
	switch mime {
	case "image/jpeg":
		return reencodeJPEG(in, maxLongEdge)
	case "image/png":
		return reencodePNG(in, maxLongEdge)
	case "image/webp":

		return reencodeJPEG(in, maxLongEdge)
	default:
		return in, "", ErrUnsupportedMIME
	}
}

func reencodeJPEG(in []byte, maxLongEdge int) ([]byte, string, error) {
	img, _, err := image.Decode(bytes.NewReader(in))
	if err != nil {
		return in, "", fmt.Errorf("decode: %w", err)
	}
	img = maybeShrink(img, maxLongEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return in, "", fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), "jpg", nil
}

func reencodePNG(in []byte, maxLongEdge int) ([]byte, string, error) {
	img, err := png.Decode(bytes.NewReader(in))
	if err != nil {
		return in, "", fmt.Errorf("decode: %w", err)
	}
	img = maybeShrink(img, maxLongEdge)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return in, "", fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), "png", nil
}

func maybeShrink(img image.Image, maxLongEdge int) image.Image {
	if maxLongEdge <= 0 {
		return img
	}
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= maxLongEdge {
		return img
	}

	var nw, nh int
	if w >= h {
		nw = maxLongEdge
		nh = h * maxLongEdge / w
		if nh < 1 {
			nh = 1
		}
	} else {
		nh = maxLongEdge
		nw = w * maxLongEdge / h
		if nw < 1 {
			nw = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, src, draw.Over, nil)
	return dst
}
