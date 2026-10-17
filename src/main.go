// gosavepic живёт в системном трее Windows и сохраняет изображения,
// скопированные в буфер обмена, в файлы PNG, JPEG, GIF или BMP в выбранной папке.
package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

const (
	appName    = "GoSavePic"
	appVersion = "1.1"

	wmTrayCallback = wmApp + 1
)

// Пункты меню значка.
const (
	idAutosave = iota + 1
	idFolder
	idOpenFolder
	idSaveAs
	idSave
	idAutostart
	idAbout
	idExit

	idFormatBase  = 100 // + индекс формата
	idQualityBase = 200 // + качество JPEG
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

type app struct {
	hwnd             uintptr
	tray             *trayIcon
	iconOn, iconOff  uintptr
	wmTaskbarCreated uint32
	pngFmt           uint32

	folder      string
	format      int
	jpegQuality int
	enabled     bool
	withText    bool
	inDialog    bool
	saved       int
	lastHash    [sha1.Size]byte
}

func main() {
	withText := flag.Bool("text", false, "автосохранять картинку, даже если вместе с ней скопирован текст (ячейки Excel, фрагменты Word)")
	logPath := flag.String("log", "", "писать журнал в файл")
	flag.Parse()

	log.SetOutput(ioutil.Discard)
	if *logPath != "" {
		if f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			defer f.Close()
			log.SetOutput(f)
		}
	}

	procSetProcessDPIAware.Call()
	if !singleInstance(`Local\GoSavePic-single-instance`) {
		messageBox(0, appName+" уже запущен — его значок находится в области уведомлений.", appName, mbIconInformation)
		return
	}

	// Окно, меню, диалоги и COM должны работать в одном потоке ОС.
	runtime.LockOSThread()
	initCOM()

	a := &app{
		folder:      loadFolder(),
		format:      loadFormat(),
		jpegQuality: loadJPEGQuality(),
		withText:    *withText,
		pngFmt:      registerClipboardFormat("PNG"),
	}
	size := smallIconSize()
	a.iconOn, a.iconOff = makeIcon(size, true), makeIcon(size, false)
	a.wmTaskbarCreated = registerWindowMessage("TaskbarCreated")

	hwnd, err := createHiddenWindow("GoSavePicWindow", syscall.NewCallback(a.wndProc))
	if err != nil {
		fatal("не удалось создать окно: " + err.Error())
	}
	a.hwnd = hwnd
	a.tray = &trayIcon{hwnd: hwnd, callback: wmTrayCallback}
	a.tray.add(a.currentIcon(), a.tooltip())

	if err := addClipboardListener(hwnd); err != nil {
		fatal("не удалось подписаться на буфер обмена: " + err.Error())
	}

	log.Printf("запущен, папка: %q", a.folder)
	runMessageLoop()
}

func fatal(text string) {
	messageBox(0, text, appName, mbIconError)
	os.Exit(1)
}

func (a *app) wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClipboardUpdate:
		if a.enabled {
			a.autoSave()
		}
		return 0
	case wmTrayCallback:
		switch lParam {
		case wmLButtonDblClk:
			a.toggleAutosave()
		case wmRButtonUp:
			a.showMenu()
		}
		return 0
	case wmDestroy:
		a.tray.remove()
		procPostQuitMessage.Call(0)
		return 0
	}
	if a.wmTaskbarCreated != 0 && message == uintptr(a.wmTaskbarCreated) {
		// Проводник перезапустился — значок нужно добавить заново.
		a.tray.add(a.currentIcon(), a.tooltip())
		return 0
	}
	return defWindowProc(hwnd, message, wParam, lParam)
}

func (a *app) showMenu() {
	if a.inDialog {
		return
	}
	folderText := "(не выбрана)"
	if a.folder != "" {
		folderText = shortenMiddle(a.folder, 50)
	}
	hasImage := a.clipboardHasImage()

	cmd := trackMenu(a.hwnd, []menuItem{
		{id: idAutosave, text: "Автосохранение", checked: a.enabled},
		{id: idFolder, text: "Папка автосохранения: " + folderText},
		{text: "Формат сохранения: " + a.formatLabel(), sub: a.formatMenu()},
		{id: idOpenFolder, text: "Открыть папку в проводнике", disabled: !dirExists(a.folder)},
		menuSeparator,
		{id: idSaveAs, text: "Сохранить как...", disabled: !hasImage},
		{id: idSave, text: "Сохранить", disabled: !hasImage},
		menuSeparator,
		{id: idAutostart, text: "Автоматически запускать при запуске системы", checked: autostartEnabled()},
		menuSeparator,
		{id: idAbout, text: "О программе..."},
		{id: idExit, text: "Выход"},
	}, idAutosave)

	switch cmd {
	case idAutosave:
		a.toggleAutosave()
	case idFolder:
		a.chooseFolder()
	case idOpenFolder:
		if err := shellOpen(a.hwnd, a.folder); err != nil {
			a.errorBox("Не удалось открыть папку: " + err.Error())
		}
	case idSaveAs:
		a.saveAs()
	case idSave:
		a.saveOnce()
	case idAutostart:
		if err := setAutostart(!autostartEnabled()); err != nil {
			a.errorBox("Не удалось изменить автозапуск: " + err.Error())
		}
	case idAbout:
		a.about()
	case idExit:
		procDestroyWindow.Call(a.hwnd)
	default:
		if cmd >= idFormatBase && cmd < idFormatBase+uintptr(len(imageFormats)) {
			a.format = int(cmd - idFormatBase)
			if err := saveFormat(a.format); err != nil {
				log.Printf("не удалось сохранить настройку формата: %v", err)
			}
			a.refreshTray()
		} else if cmd > idQualityBase && cmd <= idQualityBase+100 {
			a.jpegQuality = int(cmd - idQualityBase)
			if err := saveJPEGQuality(a.jpegQuality); err != nil {
				log.Printf("не удалось сохранить настройку качества: %v", err)
			}
			a.refreshTray()
		}
	}
}

// formatMenu — подменю выбора формата и качества JPEG. Качество действует
// и при выборе JPEG в диалоге «Сохранить как», поэтому доступно всегда.
func (a *app) formatMenu() []menuItem {
	var items []menuItem
	for i, f := range imageFormats {
		items = append(items, menuItem{id: idFormatBase + uintptr(i), text: f.name, radio: true, checked: a.format == i})
	}
	items = append(items, menuSeparator, menuItem{text: "Качество JPEG:", disabled: true})
	for _, q := range jpegQualities {
		items = append(items, menuItem{id: idQualityBase + uintptr(q), text: jpegQualityLabel(q), radio: true, checked: a.jpegQuality == q})
	}
	return items
}

func (a *app) formatLabel() string {
	if a.format == fmtJPEG {
		return fmt.Sprintf("JPEG, %d%%", a.jpegQuality)
	}
	return imageFormats[a.format].name
}

func (a *app) toggleAutosave() {
	if a.inDialog {
		return
	}
	if a.enabled {
		a.enabled = false
	} else {
		if !dirExists(a.folder) && !a.chooseFolder() {
			return
		}
		a.enabled = true
		a.lastHash = [sha1.Size]byte{}
	}
	log.Printf("автосохранение: %v", a.enabled)
	a.refreshTray()
}

// chooseFolder спрашивает папку автосохранения и запоминает её в реестре.
func (a *app) chooseFolder() bool {
	initial := a.folder
	if !dirExists(initial) {
		initial = picturesDir()
	}

	a.inDialog = true
	procSetForegroundWindow.Call(a.hwnd)
	dir, ok := pickFolder(a.hwnd, initial, "Папка для автосохранения изображений")
	a.inDialog = false
	if !ok {
		return false
	}

	a.folder = dir
	if err := saveFolder(dir); err != nil {
		log.Printf("не удалось сохранить настройку папки: %v", err)
	}
	a.refreshTray()
	return true
}

func (a *app) autoSave() {
	img, err := a.readClipboardImage(a.withText)
	if err != nil {
		log.Printf("ошибка чтения буфера: %v", err)
		return
	}
	if img == nil {
		return
	}

	// Буфер может сообщать об изменении несколько раз подряд для одной картинки.
	h := sha1.Sum(img.data)
	if h == a.lastHash {
		return
	}
	a.lastHash = h

	path := nextPath(a.folder, imageFormats[a.format].ext)
	if err := img.saveTo(path, a.format, a.jpegQuality); err != nil {
		log.Printf("ошибка сохранения %s: %v", path, err)
		a.tray.notify(appName, "Не удалось сохранить изображение: "+err.Error(), true)
		return
	}
	a.saved++
	log.Printf("сохранено: %s", path)
	a.refreshTray()
	a.tray.notify("Изображение сохранено", filepath.Base(path), false)
}

// saveOnce однократно сохраняет текущее изображение в папку автосохранения.
func (a *app) saveOnce() {
	img, ok := a.clipboardImageOrWarn()
	if !ok {
		return
	}
	if !dirExists(a.folder) && !a.chooseFolder() {
		return
	}
	path := nextPath(a.folder, imageFormats[a.format].ext)
	if err := img.saveTo(path, a.format, a.jpegQuality); err != nil {
		a.errorBox("Не удалось сохранить изображение:\n" + err.Error())
		return
	}
	log.Printf("сохранено вручную: %s", path)
	a.tray.notify("Изображение сохранено", filepath.Base(path), false)
}

func (a *app) saveAs() {
	img, ok := a.clipboardImageOrWarn()
	if !ok {
		return
	}
	initial := a.folder
	if !dirExists(initial) {
		initial = picturesDir()
	}

	a.inDialog = true
	procSetForegroundWindow.Call(a.hwnd)
	path, filterFormat, ok := saveFileDialog(a.hwnd, initial,
		filepath.Base(nextPath(initial, imageFormats[a.format].ext)), a.format)
	a.inDialog = false
	if !ok {
		return
	}
	// Формат определяется расширением, а если оно незнакомое — выбранным типом файла.
	format, known := formatByExt(filepath.Ext(path))
	if !known {
		format = filterFormat
	}
	if err := img.saveTo(path, format, a.jpegQuality); err != nil {
		a.errorBox("Не удалось сохранить изображение:\n" + err.Error())
		return
	}
	log.Printf("сохранено как: %s", path)
	a.tray.notify("Изображение сохранено", path, false)
}

func (a *app) clipboardImageOrWarn() (*clipImage, bool) {
	img, err := a.readClipboardImage(true)
	if err != nil {
		a.errorBox(err.Error())
		return nil, false
	}
	if img == nil {
		a.tray.notify(appName, "В буфере обмена нет изображения.", false)
		return nil, false
	}
	return img, true
}

func (a *app) about() {
	a.inDialog = true
	defer func() { a.inDialog = false }()

	folder := a.folder
	if folder == "" {
		folder = "не выбрана"
	}
	messageBox(a.hwnd, fmt.Sprintf(
		"%s %s\n\n"+
			"Сохраняет изображения из буфера обмена в файлы PNG, JPEG, GIF или BMP.\n\n"+
			"• Двойной щелчок по значку — включить/выключить автосохранение.\n"+
			"• Правый щелчок — меню.\n"+
			"• Зелёный значок — автосохранение включено, серый — выключено.\n\n"+
			"Папка: %s\n"+
			"Формат: %s\n"+
			"Сохранено за сеанс: %d\n\n"+
			"Собрано: %s, %s/%s",
		appName, appVersion, folder, a.formatLabel(), a.saved, runtime.Version(), runtime.GOOS, runtime.GOARCH),
		"О программе", mbOK|mbIconInformation)
}

func (a *app) errorBox(text string) {
	a.inDialog = true
	messageBox(a.hwnd, text, appName, mbIconError)
	a.inDialog = false
}

func (a *app) currentIcon() uintptr {
	if a.enabled {
		return a.iconOn
	}
	return a.iconOff
}

func (a *app) tooltip() string {
	state := "выкл."
	if a.enabled {
		state = "вкл."
	}
	tip := appName + " — автосохранение " + state + " (" + a.formatLabel() + ")"
	if a.saved > 0 {
		tip += fmt.Sprintf("\nСохранено: %d", a.saved)
	}
	if a.folder != "" {
		tip += "\n" + shortenMiddle(a.folder, 60)
	}
	return tip
}

func (a *app) refreshTray() {
	a.tray.update(a.currentIcon(), a.tooltip())
}

func (a *app) clipboardHasImage() bool {
	return (a.pngFmt != 0 && formatAvailable(a.pngFmt)) || formatAvailable(cfDIBV5) || formatAvailable(cfDIB)
}

// clipImage — изображение, прочитанное из буфера: готовый PNG или DIB.
type clipImage struct {
	data  []byte
	isPNG bool
}

// readClipboardImage берёт готовый PNG, если источник его предоставил
// (браузеры, Office, «Ножницы» — сохраняется прозрачность), иначе DIB.
func (a *app) readClipboardImage(allowText bool) (*clipImage, error) {
	if err := openClipboard(a.hwnd); err != nil {
		return nil, err
	}
	defer closeClipboard()

	if !allowText && formatAvailable(cfUnicodeText) {
		return nil, nil
	}
	if a.pngFmt != 0 && formatAvailable(a.pngFmt) {
		if b := clipboardBytes(a.pngFmt); bytes.HasPrefix(b, pngSignature) {
			return &clipImage{data: b, isPNG: true}, nil
		}
	}
	for _, f := range []uint32{cfDIBV5, cfDIB} {
		if formatAvailable(f) {
			if b := clipboardBytes(f); b != nil {
				return &clipImage{data: b}, nil
			}
		}
	}
	return nil, nil
}

func (c *clipImage) decode() (image.Image, error) {
	if c.isPNG {
		return png.Decode(bytes.NewReader(c.data))
	}
	return decodeDIB(c.data)
}

// saveTo сохраняет изображение в заданном формате (quality — для JPEG).
func (c *clipImage) saveTo(path string, format, quality int) error {
	if c.isPNG && format == fmtPNG {
		return ioutil.WriteFile(path, c.data, 0644)
	}

	img, err := c.decode()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := encodeImage(&buf, img, format, quality); err != nil {
		return err
	}
	return ioutil.WriteFile(path, buf.Bytes(), 0644)
}

func nextPath(dir, ext string) string {
	now := time.Now()
	base := fmt.Sprintf("clip_%s_%03d", now.Format("2006-01-02_15-04-05"), now.Nanosecond()/1e6)
	path := filepath.Join(dir, base+ext)
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, i, ext))
	}
}

func dirExists(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}

func picturesDir() string {
	return filepath.Join(os.Getenv("USERPROFILE"), "Pictures")
}
