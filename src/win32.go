package main

import (
	"errors"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW     = user32.NewProc("RegisterWindowMessageW")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procGetCursorPos               = user32.NewProc("GetCursorPos")
	procGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	procSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	procMessageBoxW                = user32.NewProc("MessageBoxW")
	procCreatePopupMenu            = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem         = user32.NewProc("SetMenuDefaultItem")
	procCheckMenuRadioItem         = user32.NewProc("CheckMenuRadioItem")
	procTrackPopupMenu             = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                = user32.NewProc("DestroyMenu")
	procCreateIconFromResourceEx   = user32.NewProc("CreateIconFromResourceEx")
	procAddClipboardFormatListener = user32.NewProc("AddClipboardFormatListener")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW               = kernel32.NewProc("CreateMutexW")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procShellNotifyIconW           = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW              = shell32.NewProc("ShellExecuteW")
)

const (
	wmNull            = 0x0000
	wmDestroy         = 0x0002
	wmLButtonDblClk   = 0x0203
	wmRButtonUp       = 0x0205
	wmClipboardUpdate = 0x031D
	wmApp             = 0x8000

	cfDIB         = 8
	cfUnicodeText = 13
	cfDIBV5       = 17

	csDblClks = 0x0008

	smCxSmIcon = 49

	mbOK              = 0x00
	mbIconError       = 0x10
	mbIconInformation = 0x40

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfPopup     = 0x0010
	mfByCommand = 0x0000
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	swShowNormal = 1

	errorAlreadyExists = 183
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct{ x, y int32 }

type msg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// createHiddenWindow создаёт невидимое окно верхнего уровня: оно получает
// сообщения значка в трее, «TaskbarCreated» и изменения буфера обмена.
func createHiddenWindow(className string, wndProc uintptr) (uintptr, error) {
	cls := utf16Ptr(className)
	hInst, _, _ := procGetModuleHandleW.Call(0)

	wc := wndClassEx{
		style:         csDblClks,
		lpfnWndProc:   wndProc,
		hInstance:     hInst,
		lpszClassName: cls,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return 0, err
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(cls)),
		0, 0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return 0, err
	}
	return hwnd, nil
}

func addClipboardListener(hwnd uintptr) error {
	if r, _, err := procAddClipboardFormatListener.Call(hwnd); r == 0 {
		return err
	}
	return nil
}

func defWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func runMessageLoop() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func registerWindowMessage(name string) uint32 {
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16Ptr(name))))
	return uint32(r)
}

func messageBox(hwnd uintptr, text, caption string, flags uintptr) {
	procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))), uintptr(unsafe.Pointer(utf16Ptr(caption))), flags)
}

// singleInstance возвращает false, если программа уже запущена.
func singleInstance(name string) bool {
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16Ptr(name))))
	if h == 0 {
		return true
	}
	return err != syscall.Errno(errorAlreadyExists)
}

func shellOpen(hwnd uintptr, path string) error {
	r, _, _ := procShellExecuteW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr(path))), 0, 0, swShowNormal)
	if r <= 32 {
		return syscall.Errno(r)
	}
	return nil
}

func registerClipboardFormat(name string) uint32 {
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(utf16Ptr(name))))
	return uint32(r)
}

// openClipboard открывает буфер с повторами: сразу после копирования
// его ещё может держать открытым программа-источник.
func openClipboard(hwnd uintptr) error {
	for i := 0; i < 20; i++ {
		if r, _, _ := procOpenClipboard.Call(hwnd); r != 0 {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("не удалось открыть буфер обмена")
}

func closeClipboard() {
	procCloseClipboard.Call()
}

func formatAvailable(format uint32) bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(format))
	return r != 0
}

// clipboardBytes копирует содержимое формата из открытого буфера обмена.
func clipboardBytes(format uint32) []byte {
	h, _, _ := procGetClipboardData.Call(uintptr(format))
	if h == 0 {
		return nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(h)

	n, _, _ := procGlobalSize.Call(h)
	if n == 0 || n > 1<<30 {
		return nil
	}
	src := (*[1 << 30]byte)(unsafe.Pointer(p))[:n:n]
	out := make([]byte, n)
	copy(out, src)
	return out
}
