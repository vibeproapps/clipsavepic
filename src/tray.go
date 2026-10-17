package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"strings"
	"unicode/utf16"
	"unsafe"
)

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10

	niifInfo    = 0x01
	niifError   = 0x03
	niifNoSound = 0x10
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         guid
	hBalloonIcon     uintptr
}

// trayIcon — значок программы в области уведомлений.
type trayIcon struct {
	hwnd     uintptr
	callback uint32
}

func (t *trayIcon) data(flags uint32) *notifyIconData {
	nid := &notifyIconData{hWnd: t.hwnd, uID: 1, uFlags: flags}
	nid.cbSize = uint32(unsafe.Sizeof(*nid))
	return nid
}

func (t *trayIcon) add(icon uintptr, tip string) bool {
	nid := t.data(nifMessage | nifIcon | nifTip)
	nid.uCallbackMessage = t.callback
	nid.hIcon = icon
	copyUTF16(nid.szTip[:], tip)
	r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(nid)))
	return r != 0
}

func (t *trayIcon) update(icon uintptr, tip string) {
	nid := t.data(nifIcon | nifTip)
	nid.hIcon = icon
	copyUTF16(nid.szTip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(nid)))
}

// notify показывает всплывающее уведомление от значка.
func (t *trayIcon) notify(title, text string, isError bool) {
	nid := t.data(nifInfo)
	copyUTF16(nid.szInfoTitle[:], title)
	copyUTF16(nid.szInfo[:], text)
	nid.dwInfoFlags = niifInfo | niifNoSound
	if isError {
		nid.dwInfoFlags = niifError
	}
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(nid)))
}

func (t *trayIcon) remove() {
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(t.data(0))))
}

func copyUTF16(dst []uint16, s string) {
	u := utf16.Encode([]rune(s))
	if len(u) > len(dst)-1 {
		u = u[:len(dst)-1]
	}
	copy(dst, u)
	dst[len(u)] = 0
}

type menuItem struct {
	id        uintptr
	text      string
	checked   bool
	radio     bool // отметка точкой (выбор одного из группы) вместо галочки
	disabled  bool
	separator bool
	sub       []menuItem
}

var menuSeparator = menuItem{separator: true}

func buildMenu(items []menuItem) uintptr {
	m, _, _ := procCreatePopupMenu.Call()
	for _, it := range items {
		if it.separator {
			procAppendMenuW.Call(m, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.checked && !it.radio {
			flags |= mfChecked
		}
		if it.disabled {
			flags |= mfGrayed
		}
		id := it.id
		if it.sub != nil {
			flags |= mfPopup
			id = buildMenu(it.sub)
		}
		// «&» в тексте меню — признак горячей клавиши, его нужно удвоить.
		text := strings.Replace(it.text, "&", "&&", -1)
		procAppendMenuW.Call(m, flags, id, uintptr(unsafe.Pointer(utf16Ptr(text))))
		if it.radio && it.checked {
			procCheckMenuRadioItem.Call(m, it.id, it.id, it.id, mfByCommand)
		}
	}
	return m
}

// trackMenu показывает контекстное меню у курсора и возвращает id выбранного пункта (0 — отмена).
func trackMenu(hwnd uintptr, items []menuItem, defaultID uintptr) uintptr {
	m := buildMenu(items)
	defer procDestroyMenu.Call(m) // подменю уничтожаются вместе с ним
	procSetMenuDefaultItem.Call(m, defaultID, 0)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Без этого меню не закрывается при щелчке мимо него.
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(m, tpmRightButton|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.x), uintptr(pt.y), 0, hwnd, 0)
	procPostMessageW.Call(hwnd, wmNull, 0, 0)
	return cmd
}

// shortenMiddle сокращает строку до max символов, заменяя середину на «...».
func shortenMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := (max - 3) / 2
	tail := max - 3 - head
	return string(r[:head]) + "..." + string(r[len(r)-tail:])
}

func smallIconSize() int {
	n, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if n == 0 {
		return 16
	}
	return int(n)
}

// makeIcon рисует значок «картинка» (зелёный — вкл, серый — выкл) и создаёт HICON.
func makeIcon(size int, on bool) uintptr {
	img := drawIcon(size, on)

	maskStride := ((size + 31) / 32) * 4
	pixBytes := size * size * 4
	buf := make([]byte, 40+pixBytes+maskStride*size)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], 40)
	le.PutUint32(buf[4:], uint32(size))
	le.PutUint32(buf[8:], uint32(size*2)) // XOR-изображение + AND-маска
	le.PutUint16(buf[12:], 1)
	le.PutUint16(buf[14:], 32)
	le.PutUint32(buf[20:], uint32(len(buf)-40))

	pix := buf[40:]
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := img.NRGBAAt(x, size-1-y) // DIB хранится снизу вверх
			i := (y*size + x) * 4
			pix[i], pix[i+1], pix[i+2], pix[i+3] = c.B, c.G, c.R, c.A
		}
	}
	// AND-маска остаётся нулевой: прозрачность задаёт альфа-канал.

	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		1, 0x00030000, uintptr(size), uintptr(size), 0)
	return h
}

func drawIcon(size int, on bool) *image.NRGBA {
	body := color.NRGBA{0x8A, 0x8A, 0x8A, 0xFF}
	fg := color.NRGBA{0xEE, 0xEE, 0xEE, 0xFF}
	if on {
		body = color.NRGBA{0x1F, 0x9D, 0x55, 0xFF}
		fg = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	}

	shade := func(u, v float64) (color.NRGBA, bool) {
		if !inRoundRect(u, v, 0.04, 0.14, 0.96, 0.86, 0.14) {
			return color.NRGBA{}, false
		}
		sun := (u-0.70)*(u-0.70)+(v-0.34)*(v-0.34) <= 0.09*0.09
		if sun ||
			inTriangle(u, v, 0.14, 0.78, 0.40, 0.42, 0.64, 0.78) ||
			inTriangle(u, v, 0.46, 0.78, 0.66, 0.54, 0.86, 0.78) {
			return fg, true
		}
		return body, true
	}

	// Сглаживание: 4×4 выборки на пиксель.
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					v := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					if c, ok := shade(u, v); ok {
						r += float64(c.R)
						g += float64(c.G)
						b += float64(c.B)
						a++
					}
				}
			}
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{
					uint8(r / a), uint8(g / a), uint8(b / a), uint8(a * 255 / (ss * ss)),
				})
			}
		}
	}
	return img
}

func inRoundRect(u, v, x0, y0, x1, y1, rad float64) bool {
	if u < x0 || u > x1 || v < y0 || v > y1 {
		return false
	}
	dx, dy := 0.0, 0.0
	if u < x0+rad {
		dx = x0 + rad - u
	} else if u > x1-rad {
		dx = u - (x1 - rad)
	}
	if v < y0+rad {
		dy = y0 + rad - v
	} else if v > y1-rad {
		dy = v - (y1 - rad)
	}
	return dx*dx+dy*dy <= rad*rad
}

func inTriangle(px, py, ax, ay, bx, by, cx, cy float64) bool {
	d1 := (px-bx)*(ay-by) - (ax-bx)*(py-by)
	d2 := (px-cx)*(by-cy) - (bx-cx)*(py-cy)
	d3 := (px-ax)*(cy-ay) - (cx-ax)*(py-ay)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}
