package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	settingsKey  = `Software\GoSavePic`
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "GoSavePic"
)

var (
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = advapi32.NewProc("RegDeleteValueW")
)

func regGetString(path, name string) (string, bool) {
	var h syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, utf16Ptr(path), 0, syscall.KEY_READ, &h) != nil {
		return "", false
	}
	defer syscall.RegCloseKey(h)

	var typ, n uint32
	if syscall.RegQueryValueEx(h, utf16Ptr(name), nil, &typ, nil, &n) != nil || typ != syscall.REG_SZ {
		return "", false
	}
	buf := make([]uint16, n/2+1)
	if syscall.RegQueryValueEx(h, utf16Ptr(name), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}

func regOpenForWrite(path string) (syscall.Handle, error) {
	var h syscall.Handle
	r, _, _ := procRegCreateKeyExW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(utf16Ptr(path))),
		0, 0, 0, syscall.KEY_READ|syscall.KEY_WRITE, 0, uintptr(unsafe.Pointer(&h)), 0)
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return h, nil
}

func regSetString(path, name, value string) error {
	h, err := regOpenForWrite(path)
	if err != nil {
		return err
	}
	defer syscall.RegCloseKey(h)

	data, _ := syscall.UTF16FromString(value)
	r, _, _ := procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(utf16Ptr(name))), 0, syscall.REG_SZ,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func regDeleteValue(path, name string) error {
	h, err := regOpenForWrite(path)
	if err != nil {
		return err
	}
	defer syscall.RegCloseKey(h)

	r, _, _ := procRegDeleteValueW.Call(uintptr(h), uintptr(unsafe.Pointer(utf16Ptr(name))))
	if r != 0 && r != uintptr(syscall.ERROR_FILE_NOT_FOUND) {
		return syscall.Errno(r)
	}
	return nil
}

func loadFolder() string {
	s, _ := regGetString(settingsKey, "Folder")
	return s
}

func saveFolder(dir string) error {
	return regSetString(settingsKey, "Folder", dir)
}

func loadFormat() int {
	s, _ := regGetString(settingsKey, "Format")
	return formatByName(s)
}

func saveFormat(format int) error {
	return regSetString(settingsKey, "Format", imageFormats[format].name)
}

func loadJPEGQuality() int {
	s, _ := regGetString(settingsKey, "JpegQuality")
	if q, err := strconv.Atoi(s); err == nil && q >= 1 && q <= 100 {
		return q
	}
	return defaultJPEGQuality
}

func saveJPEGQuality(q int) error {
	return regSetString(settingsKey, "JpegQuality", strconv.Itoa(q))
}

func autostartCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return `"` + exe + `"`
}

// autostartEnabled проверяет, что в автозапуске прописан именно этот exe.
func autostartEnabled() bool {
	v, ok := regGetString(runKey, runValueName)
	return ok && strings.EqualFold(v, autostartCommand())
}

func setAutostart(on bool) error {
	if !on {
		return regDeleteValue(runKey, runValueName)
	}
	return regSetString(runKey, runValueName, autostartCommand())
}
