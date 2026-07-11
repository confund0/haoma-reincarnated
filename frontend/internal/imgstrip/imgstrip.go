package imgstrip

import (
	"bytes"
	"encoding/binary"
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

	img = applyOrientation(img, orientationOf(in))
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

func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)

	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	for sy := 0; sy < h; sy++ {
		for sx := 0; sx < w; sx++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-sx, sy
			case 3:
				dx, dy = w-1-sx, h-1-sy
			case 4:
				dx, dy = sx, h-1-sy
			case 5:
				dx, dy = sy, sx
			case 6:
				dx, dy = h-1-sy, sx
			case 7:
				dx, dy = h-1-sy, w-1-sx
			case 8:
				dx, dy = sy, w-1-sx
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

func orientationOf(in []byte) int {
	if len(in) < 4 || in[0] != 0xFF || in[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+2 <= len(in) {
		if in[i] != 0xFF {
			return 1
		}
		marker := in[i+1]

		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9) {
			i += 2
			continue
		}
		if i+4 > len(in) {
			return 1
		}
		segLen := int(in[i+2])<<8 | int(in[i+3])
		if segLen < 2 || i+2+segLen > len(in) {
			return 1
		}
		if marker == 0xE1 {
			if o, ok := orientationFromExif(in[i+4 : i+2+segLen]); ok {
				return o
			}
		}
		if marker == 0xDA {
			return 1
		}
		i += 2 + segLen
	}
	return 1
}

func orientationFromExif(seg []byte) (int, bool) {
	if len(seg) < 8 || string(seg[:6]) != "Exif\x00\x00" {
		return 0, false
	}
	t := seg[6:]
	if len(t) < 8 {
		return 0, false
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0, false
	}
	if bo.Uint16(t[2:4]) != 0x002A {
		return 0, false
	}
	ifd := int(bo.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0, false
	}
	count := int(bo.Uint16(t[ifd:]))
	entry := ifd + 2
	for k := 0; k < count; k++ {
		if entry+12 > len(t) {
			return 0, false
		}
		if bo.Uint16(t[entry:]) == 0x0112 {
			o := int(bo.Uint16(t[entry+8:]))
			if o >= 1 && o <= 8 {
				return o, true
			}
			return 0, false
		}
		entry += 12
	}
	return 0, false
}
