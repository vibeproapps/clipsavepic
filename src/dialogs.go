package main

import (
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	ole32    = syscall.NewLazyDLL("ole32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")

	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procGetSaveFileNameW            = comdlg32.NewProc("GetSaveFileNameW")
)

var (
	clsidFileOpenDialog = guid{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = guid{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = guid{0x43826D1E, 0xE718, 0x42EE, [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4
	clsctxInprocServer      = 0x1

	fosPickFolders     = 0x00000020
	fosForceFileSystem = 0x00000040
	fosPathMustExist   = 0x00000800

	sigdnFileSysPath = 0x80058000

	// Номера методов в таблицах виртуальных функций COM.
	vtRelease            = 2
	vtShow               = 3
	vtSetOptions         = 9
	vtGetOptions         = 10
	vtSetFolder          = 12
	vtSetTitle           = 17
	vtGetResult          = 20
	vtItemGetDisplayName = 5

	ofnOverwritePrompt = 0x00000002
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnExplorer        = 0x00080000
)

func initCOM() {
	procCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
}

func comCall(obj uintptr, method int, args ...uintptr) int32 {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(method)*unsafe.Sizeof(uintptr(0))))
	var a [5]uintptr
	copy(a[:], args)
	r, _, _ := syscall.Syscall6(fn, uintptr(len(args)+1), obj, a[0], a[1], a[2], a[3], a[4])
	return int32(r)
}

// pickFolder показывает стандартный диалог выбора папки Windows.
func pickFolder(owner uintptr, initial, title string) (string, bool) {
	var dlg uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if int32(hr) < 0 || dlg == 0 {
		return "", false
	}
	defer comCall(dlg, vtRelease)

	var opts uint32
	comCall(dlg, vtGetOptions, uintptr(unsafe.Pointer(&opts)))
	comCall(dlg, vtSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem|fosPathMustExist))

	titlePtr := utf16Ptr(title)
	comCall(dlg, vtSetTitle, uintptr(unsafe.Pointer(titlePtr)))
	runtime.KeepAlive(titlePtr)

	if initial != "" && dirExists(initial) {
		var item uintptr
		r, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(utf16Ptr(initial))), 0,
			uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item)))
		if int32(r) >= 0 && item != 0 {
			comCall(dlg, vtSetFolder, item)
			comCall(item, vtRelease)
		}
	}

	if comCall(dlg, vtShow, owner) < 0 { // в т.ч. отмена пользователем
		return "", false
	}

	var item uintptr
	if comCall(dlg, vtGetResult, uintptr(unsafe.Pointer(&item))) < 0 || item == 0 {
		return "", false
	}
	defer comCall(item, vtRelease)

	var p uintptr
	if comCall(item, vtItemGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))) < 0 || p == 0 {
		return "", false
	}
	defer procCoTaskMemFree.Call(p)
	return utf16PtrToString(p), true
}

func utf16PtrToString(p uintptr) string {
	var s []uint16
	for ; ; p += 2 {
		c := *(*uint16)(unsafe.Pointer(p))
		if c == 0 {
			break
		}
		s = append(s, c)
	}
	return string(utf16.Decode(s))
}

type openFileName struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

// saveFileDialog показывает диалог «Сохранить как» со всеми форматами;
// format — тип файла, выбранный по умолчанию. Возвращает путь и выбранный тип.
func saveFileDialog(owner uintptr, initialDir, fileName string, format int) (string, int, bool) {
	var filterStr string
	for _, f := range imageFormats {
		filterStr += f.name + " (" + f.pattern + ")\x00" + f.pattern + "\x00"
	}
	filter := utf16.Encode([]rune(filterStr + "\x00"))
	file := make([]uint16, 4096)
	copy(file, utf16.Encode([]rune(fileName)))

	ofn := &openFileName{
		hwndOwner:    owner,
		lpstrFilter:  &filter[0],
		nFilterIndex: uint32(format + 1),
		lpstrFile:    &file[0],
		nMaxFile:     uint32(len(file)),
		lpstrTitle:   utf16Ptr("Сохранить изображение из буфера обмена"),
		// С непустым lpstrDefExt диалог подставляет расширение выбранного типа файла.
		lpstrDefExt: utf16Ptr(imageFormats[format].ext[1:]),
		flags:       ofnOverwritePrompt | ofnNoChangeDir | ofnPathMustExist | ofnExplorer,
	}
	if initialDir != "" {
		ofn.lpstrInitialDir = utf16Ptr(initialDir)
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(*ofn))

	r, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(ofn)))
	runtime.KeepAlive(filter)
	runtime.KeepAlive(file)
	if r == 0 {
		return "", 0, false
	}
	chosen := int(ofn.nFilterIndex) - 1
	if chosen < 0 || chosen >= len(imageFormats) {
		chosen = format
	}
	return syscall.UTF16ToString(file), chosen, true
}
