package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// testImage: градиент (больше 256 цветов) и прозрачный левый верхний угол.
func testImage(w, h int, gradient bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{200, 30, 30, 255}
			if gradient {
				c = color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255}
			}
			if x < 10 && y < 10 {
				c = color.NRGBA{}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func encode(t *testing.T, img image.Image, format int) []byte {
	var buf bytes.Buffer
	if err := encodeImage(&buf, img, format, 90); err != nil {
		t.Fatalf("%s: %v", imageFormats[format].name, err)
	}
	return buf.Bytes()
}

func near(a, b uint8, tol int) bool {
	d := int(a) - int(b)
	return d >= -tol && d <= tol
}

func TestBMPRoundTrip(t *testing.T) {
	for _, alpha := range []bool{false, true} {
		src := testImage(37, 21, true)
		if !alpha {
			src.SetNRGBA(0, 0, color.NRGBA{1, 2, 3, 255})
			for y := 0; y < 10; y++ {
				for x := 0; x < 10; x++ {
					src.SetNRGBA(x, y, color.NRGBA{1, 2, 3, 255})
				}
			}
		}
		data := encode(t, src, fmtBMP)
		got, err := decodeDIB(data[14:]) // без BITMAPFILEHEADER это тот же DIB, что в буфере обмена
		if err != nil {
			t.Fatal(err)
		}
		n := toNRGBA(got)
		if n.Bounds() != src.Bounds() || !bytes.Equal(n.Pix, src.Pix) {
			t.Errorf("BMP (alpha=%v): пиксели не совпадают", alpha)
		}
	}
}

func TestJPEG(t *testing.T) {
	got, err := jpeg.Decode(bytes.NewReader(encode(t, testImage(64, 64, false), fmtJPEG)))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := got.At(1, 1).RGBA() // прозрачный угол → белый фон
	if r>>8 < 245 || g>>8 < 245 || b>>8 < 245 {
		t.Errorf("прозрачная область должна стать белой, получено %d %d %d", r>>8, g>>8, b>>8)
	}
}

func TestGIF(t *testing.T) {
	for _, gradient := range []bool{false, true} {
		src := testImage(80, 60, gradient)
		g, err := gif.Decode(bytes.NewReader(encode(t, src, fmtGIF)))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, a := g.At(2, 2).RGBA(); a != 0 {
			t.Errorf("gradient=%v: угол должен быть прозрачным", gradient)
		}
		want := src.NRGBAAt(50, 40)
		r, gg, b, _ := g.At(50, 40).RGBA()
		tol := 0
		if gradient {
			tol = 24
		}
		if !near(uint8(r>>8), want.R, tol) || !near(uint8(gg>>8), want.G, tol) || !near(uint8(b>>8), want.B, tol) {
			t.Errorf("gradient=%v: цвет %d,%d,%d, ожидался около %v", gradient, r>>8, gg>>8, b>>8, want)
		}
	}
}

func TestPNG(t *testing.T) {
	src := testImage(30, 30, true)
	got, err := png.Decode(bytes.NewReader(encode(t, src, fmtPNG)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(toNRGBA(got).Pix, src.Pix) {
		t.Error("PNG: пиксели не совпадают")
	}
}

func TestFormatByExt(t *testing.T) {
	cases := map[string]int{".PNG": fmtPNG, ".jpeg": fmtJPEG, ".jpg": fmtJPEG, ".gif": fmtGIF, ".bmp": fmtBMP}
	for ext, want := range cases {
		if got, ok := formatByExt(ext); !ok || got != want {
			t.Errorf("%s → %d, %v", ext, got, ok)
		}
	}
	if _, ok := formatByExt(".webp"); ok {
		t.Error(".webp не должен распознаваться")
	}
}
