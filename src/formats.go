package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"sort"
	"strconv"
	"strings"
)

type imageFormat struct {
	name    string
	ext     string // расширение по умолчанию
	pattern string // маска для диалога «Сохранить как»
}

const (
	fmtPNG = iota
	fmtJPEG
	fmtGIF
	fmtBMP
)

var imageFormats = []imageFormat{
	fmtPNG:  {"PNG", ".png", "*.png"},
	fmtJPEG: {"JPEG", ".jpg", "*.jpg;*.jpeg"},
	fmtGIF:  {"GIF", ".gif", "*.gif"},
	fmtBMP:  {"BMP", ".bmp", "*.bmp"},
}

const defaultJPEGQuality = 90

var jpegQualities = []int{100, 95, 90, 80, 70, 50}

func jpegQualityLabel(q int) string {
	switch q {
	case 100:
		return "100% — наилучшее"
	case defaultJPEGQuality:
		return "90% — рекомендуется"
	case 50:
		return "50% — наименьший размер"
	}
	return strconv.Itoa(q) + "%"
}

func formatByName(name string) int {
	for i, f := range imageFormats {
		if strings.EqualFold(f.name, name) {
			return i
		}
	}
	return fmtPNG
}

func formatByExt(ext string) (int, bool) {
	ext = strings.ToLower(ext)
	if ext == ".jpeg" {
		return fmtJPEG, true
	}
	for i, f := range imageFormats {
		if f.ext == ext {
			return i, true
		}
	}
	return 0, false
}

func encodeImage(w io.Writer, img image.Image, format, quality int) error {
	switch format {
	case fmtJPEG:
		return jpeg.Encode(w, flattenOnWhite(img), &jpeg.Options{Quality: quality})
	case fmtGIF:
		return gif.Encode(w, toPaletted(img), nil)
	case fmtBMP:
		return encodeBMP(w, img)
	default:
		return png.Encode(w, img)
	}
}

// flattenOnWhite подкладывает белый фон: в JPEG нет прозрачности.
func flattenOnWhite(img image.Image) *image.RGBA {
	b := img.Bounds()
	bg := image.NewRGBA(b)
	draw.Draw(bg, b, image.NewUniform(color.White), image.ZP, draw.Src)
	draw.Draw(bg, b, img, b.Min, draw.Over)
	return bg
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	b := img.Bounds()
	n := image.NewNRGBA(b)
	draw.Draw(n, b, img, b.Min, draw.Src)
	return n
}

// encodeBMP пишет 24-битный BMP, а при наличии прозрачности — 32-битный с альфа-каналом.
func encodeBMP(w io.Writer, img image.Image) error {
	src := toNRGBA(img)
	b := src.Bounds()
	width, height := b.Dx(), b.Dy()

	hasAlpha := false
	for i := 3; i < len(src.Pix); i += 4 {
		if src.Pix[i] != 255 {
			hasAlpha = true
			break
		}
	}

	bpp, hdrSize := 24, 40
	if hasAlpha {
		bpp, hdrSize = 32, 108 // BITMAPV4HEADER с масками каналов
	}
	stride := ((width*bpp + 31) / 32) * 4
	pixOffset := 14 + hdrSize
	buf := make([]byte, pixOffset+stride*height)
	le := binary.LittleEndian

	buf[0], buf[1] = 'B', 'M'
	le.PutUint32(buf[2:], uint32(len(buf)))
	le.PutUint32(buf[10:], uint32(pixOffset))

	h := buf[14:]
	le.PutUint32(h[0:], uint32(hdrSize))
	le.PutUint32(h[4:], uint32(width))
	le.PutUint32(h[8:], uint32(height)) // положительная высота — строки снизу вверх
	le.PutUint16(h[12:], 1)
	le.PutUint16(h[14:], uint16(bpp))
	le.PutUint32(h[20:], uint32(stride*height))
	le.PutUint32(h[24:], 3780) // 96 DPI
	le.PutUint32(h[28:], 3780)
	if hasAlpha {
		le.PutUint32(h[16:], biBitfields)
		le.PutUint32(h[40:], 0x00FF0000)
		le.PutUint32(h[44:], 0x0000FF00)
		le.PutUint32(h[48:], 0x000000FF)
		le.PutUint32(h[52:], 0xFF000000)
		le.PutUint32(h[56:], 0x73524742) // LCS_sRGB
	}

	pix := buf[pixOffset:]
	for y := 0; y < height; y++ {
		row := pix[(height-1-y)*stride:]
		s := src.Pix[y*src.Stride:]
		for x := 0; x < width; x++ {
			r, g, bl, a := s[x*4], s[x*4+1], s[x*4+2], s[x*4+3]
			if hasAlpha {
				row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = bl, g, r, a
			} else {
				row[x*3], row[x*3+1], row[x*3+2] = bl, g, r
			}
		}
	}
	_, err := w.Write(buf)
	return err
}

// toPaletted готовит изображение для GIF: если цветов не больше 256, палитра
// точная; иначе подбирается медианным сечением с диффузией ошибки.
// Пиксели с альфой < 128 становятся прозрачными.
func toPaletted(img image.Image) *image.Paletted {
	src := toNRGBA(img)
	b := src.Bounds()

	hasTransparent := false
	uniq := make(map[uint32]uint8)
	overflow := false
	for i := 0; i < len(src.Pix); i += 4 {
		if src.Pix[i+3] < 128 {
			hasTransparent = true
			continue
		}
		if overflow {
			continue
		}
		key := uint32(src.Pix[i])<<16 | uint32(src.Pix[i+1])<<8 | uint32(src.Pix[i+2])
		if _, ok := uniq[key]; !ok {
			if len(uniq) == 256 {
				overflow = true
				continue
			}
			uniq[key] = uint8(len(uniq))
		}
	}

	maxColors := 256
	if hasTransparent {
		maxColors = 255
	}

	var dst *image.Paletted
	if !overflow && len(uniq) <= maxColors {
		pal := make(color.Palette, len(uniq))
		for key, idx := range uniq {
			pal[idx] = color.RGBA{uint8(key >> 16), uint8(key >> 8), uint8(key), 255}
		}
		dst = image.NewPaletted(b, pal)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				p := src.Pix[src.PixOffset(x, y):]
				if p[3] >= 128 {
					dst.SetColorIndex(x, y, uniq[uint32(p[0])<<16|uint32(p[1])<<8|uint32(p[2])])
				}
			}
		}
	} else {
		opaque := image.NewRGBA(b)
		for i := 0; i < len(src.Pix); i += 4 {
			copy(opaque.Pix[i:i+3], src.Pix[i:i+3])
			opaque.Pix[i+3] = 255
		}
		dst = image.NewPaletted(b, medianCut(src, maxColors))
		draw.FloydSteinberg.Draw(dst, b, opaque, b.Min)
	}

	if hasTransparent {
		ti := uint8(len(dst.Palette))
		dst.Palette = append(dst.Palette, color.RGBA{})
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if src.Pix[src.PixOffset(x, y)+3] < 128 {
					dst.SetColorIndex(x, y, ti)
				}
			}
		}
	}
	return dst
}

type colorBin struct {
	c [3]float64 // средний цвет ячейки
	n uint64     // число пикселей
}

// medianCut строит палитру до n цветов по гистограмме с точностью 5 бит на канал.
func medianCut(src *image.NRGBA, n int) color.Palette {
	const bins = 32 * 32 * 32
	count := make([]uint64, bins)
	sum := make([][3]uint64, bins)
	for i := 0; i < len(src.Pix); i += 4 {
		if src.Pix[i+3] < 128 {
			continue
		}
		r, g, b := src.Pix[i], src.Pix[i+1], src.Pix[i+2]
		k := int(r>>3)<<10 | int(g>>3)<<5 | int(b>>3)
		count[k]++
		sum[k][0] += uint64(r)
		sum[k][1] += uint64(g)
		sum[k][2] += uint64(b)
	}

	var all []colorBin
	for k, c := range count {
		if c > 0 {
			f := float64(c)
			all = append(all, colorBin{[3]float64{float64(sum[k][0]) / f, float64(sum[k][1]) / f, float64(sum[k][2]) / f}, c})
		}
	}
	if len(all) == 0 {
		return color.Palette{color.RGBA{0, 0, 0, 255}}
	}

	boxes := [][]colorBin{all}
	for len(boxes) < n {
		best, bestAxis, bestScore := -1, 0, 0.0
		for i, box := range boxes {
			if len(box) < 2 {
				continue
			}
			axis, rng := longestAxis(box)
			var pop uint64
			for _, e := range box {
				pop += e.n
			}
			if score := rng * float64(pop); score > bestScore {
				best, bestAxis, bestScore = i, axis, score
			}
		}
		if best < 0 {
			break
		}

		box := boxes[best]
		sort.Slice(box, func(i, j int) bool { return box[i].c[bestAxis] < box[j].c[bestAxis] })
		var total, acc uint64
		for _, e := range box {
			total += e.n
		}
		split := 1
		for i, e := range box {
			acc += e.n
			if acc*2 >= total {
				split = i + 1
				break
			}
		}
		if split >= len(box) {
			split = len(box) - 1
		}
		boxes[best] = box[:split]
		boxes = append(boxes, box[split:])
	}

	pal := make(color.Palette, 0, len(boxes))
	for _, box := range boxes {
		var r, g, b, w float64
		for _, e := range box {
			f := float64(e.n)
			r += e.c[0] * f
			g += e.c[1] * f
			b += e.c[2] * f
			w += f
		}
		pal = append(pal, color.RGBA{uint8(r/w + 0.5), uint8(g/w + 0.5), uint8(b/w + 0.5), 255})
	}
	return pal
}

func longestAxis(box []colorBin) (int, float64) {
	lo := [3]float64{255, 255, 255}
	hi := [3]float64{}
	for _, e := range box {
		for a := 0; a < 3; a++ {
			if e.c[a] < lo[a] {
				lo[a] = e.c[a]
			}
			if e.c[a] > hi[a] {
				hi[a] = e.c[a]
			}
		}
	}
	axis := 0
	for a := 1; a < 3; a++ {
		if hi[a]-lo[a] > hi[axis]-lo[axis] {
			axis = a
		}
	}
	return axis, hi[axis] - lo[axis]
}
