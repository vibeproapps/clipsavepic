package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math/bits"
)

const (
	biRGB            = 0
	biBitfields      = 3
	biAlphaBitfields = 6
)

func le16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off:]) }
func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

// decodeDIB разбирает CF_DIB / CF_DIBV5 (BITMAPINFOHEADER и новее + пиксели).
func decodeDIB(b []byte) (image.Image, error) {
	if len(b) < 40 {
		return nil, errors.New("DIB слишком короткий")
	}
	hdr := int(le32(b, 0))
	if hdr < 40 || hdr > len(b) {
		return nil, fmt.Errorf("неподдерживаемый заголовок DIB (%d байт)", hdr)
	}
	width := int(int32(le32(b, 4)))
	height := int(int32(le32(b, 8)))
	bpp := int(le16(b, 14))
	comp := le32(b, 16)
	clrUsed := int(le32(b, 32))

	topDown := height < 0
	if topDown {
		height = -height
	}
	if width <= 0 || height <= 0 || width > 1<<16 || height > 1<<16 {
		return nil, fmt.Errorf("некорректный размер %dx%d", width, height)
	}

	off := hdr
	var rm, gm, bm, am uint32
	switch comp {
	case biRGB:
		switch bpp {
		case 16:
			rm, gm, bm = 0x7C00, 0x03E0, 0x001F
		case 24, 32:
			rm, gm, bm = 0xFF0000, 0x00FF00, 0x0000FF
			if bpp == 32 {
				am = 0xFF000000
			}
		}
	case biBitfields, biAlphaBitfields:
		if hdr >= 52 {
			// BITMAPV2+ хранит маски внутри заголовка.
			rm, gm, bm = le32(b, 40), le32(b, 44), le32(b, 48)
			if hdr >= 56 {
				am = le32(b, 52)
			}
		} else {
			n := 3
			if comp == biAlphaBitfields {
				n = 4
			}
			if len(b) < off+n*4 {
				return nil, errors.New("DIB обрезан (маски)")
			}
			rm, gm, bm = le32(b, off), le32(b, off+4), le32(b, off+8)
			if n == 4 {
				am = le32(b, off+12)
			}
			off += n * 4
		}
	default:
		return nil, fmt.Errorf("неподдерживаемое сжатие DIB: %d", comp)
	}

	var palette [][4]uint8
	switch bpp {
	case 1, 4, 8:
		n := clrUsed
		if n == 0 || n > 1<<uint(bpp) {
			n = 1 << uint(bpp)
		}
		if len(b) < off+n*4 {
			return nil, errors.New("DIB обрезан (палитра)")
		}
		palette = make([][4]uint8, n)
		for i := range palette {
			p := b[off+i*4:]
			palette[i] = [4]uint8{p[2], p[1], p[0], 255}
		}
		off += n * 4
	case 16, 24, 32:
		off += clrUsed * 4
	default:
		return nil, fmt.Errorf("неподдерживаемая глубина цвета: %d бит", bpp)
	}

	stride := ((width*bpp + 31) / 32) * 4
	size := stride * height
	if off+size > len(b) {
		// Некоторые программы пишут заголовок неаккуратно — берём пиксели с конца блока.
		alt := len(b) - size
		if alt < hdr {
			return nil, errors.New("DIB обрезан (пиксели)")
		}
		off = alt
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	hasAlpha := false
	for y := 0; y < height; y++ {
		row := b[off+y*stride : off+(y+1)*stride]
		dy := height - 1 - y
		if topDown {
			dy = y
		}
		dst := img.Pix[dy*img.Stride:]
		for x := 0; x < width; x++ {
			var r, g, bl, a uint8 = 0, 0, 0, 255
			switch bpp {
			case 32:
				v := le32(row, x*4)
				r, g, bl = extract(v, rm), extract(v, gm), extract(v, bm)
				if am != 0 {
					a = extract(v, am)
					if a != 0 {
						hasAlpha = true
					}
				}
			case 24:
				bl, g, r = row[x*3], row[x*3+1], row[x*3+2]
			case 16:
				v := uint32(le16(row, x*2))
				r, g, bl = extract(v, rm), extract(v, gm), extract(v, bm)
			default:
				bit := x * bpp
				shift := uint(8 - bpp - bit%8)
				idx := int(row[bit/8]>>shift) & (1<<uint(bpp) - 1)
				if idx < len(palette) {
					c := palette[idx]
					r, g, bl = c[0], c[1], c[2]
				}
			}
			p := dst[x*4 : x*4+4]
			p[0], p[1], p[2], p[3] = r, g, bl, a
		}
	}

	// Альфа-канал часто не заполнен (всё нули) — тогда изображение непрозрачное.
	if am != 0 && !hasAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	return img, nil
}

// extract достаёт компонент цвета по маске и масштабирует его до 8 бит.
func extract(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := uint(bits.TrailingZeros32(mask))
	n := uint(bits.OnesCount32(mask))
	x := (v & mask) >> shift
	if n >= 8 {
		return uint8(x >> (n - 8))
	}
	return uint8(x * 255 / (1<<n - 1))
}
