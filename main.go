package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/google/gousb"
	"go.bug.st/serial"
)

var customFont fyne.Resource

func init() {
	res, err := fyne.LoadResourceFromPath("fonts/samsungsharpsans-bold.otf")
	if err == nil {
		customFont = res
	}
}

// --- Helper Functions (Ymir & CSV) ---

func parseYmirOutput(output string) map[string]string {
	data := make(map[string]string)
	var infoLine string
	// Veri genellikle @# ile başlar ve #@ ile biter
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "@#") {
			infoLine = line
			break
		}
	}
	if infoLine == "" {
		return data
	}

	cleaned := strings.TrimSpace(infoLine)
	cleaned = strings.TrimPrefix(cleaned, "@#")
	cleaned = strings.TrimSuffix(cleaned, "#@")
	cleaned = strings.TrimSpace(cleaned)

	pairs := strings.Split(cleaned, ";")
	for _, pair := range pairs {
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			data[parts[0]] = parts[1]
		}
	}
	return data
}

func findSamsungModem() string {
	ports, _ := filepath.Glob("/dev/ttyACM*")
	if len(ports) > 0 {
		return ports[0]
	}
	return ""
}

func sendATCommand(portName string, command string) string {
	mode := &serial.Mode{
		BaudRate: 115200,
	}
	p, err := serial.Open(portName, mode)
	if err != nil {
		return ""
	}
	defer p.Close()

	_, err = p.Write([]byte(command + "\r\n"))
	if err != nil {
		return ""
	}

	time.Sleep(200 * time.Millisecond)
	buf := make([]byte, 2048)
	n, _ := p.Read(buf)
	return string(buf[:n])
}

func parseATDevConInfo(data string) (model string, serialNum string) {
	// Örn: MN(SM-X210);...;SN(R92X10D8EYL);
	if mnStart := strings.Index(data, "MN("); mnStart != -1 {
		rest := data[mnStart+3:]
		if mnEnd := strings.Index(rest, ")"); mnEnd != -1 {
			model = rest[:mnEnd]
		}
	}
	if snStart := strings.Index(data, "SN("); snStart != -1 {
		rest := data[snStart+3:]
		if snEnd := strings.Index(rest, ")"); snEnd != -1 {
			serialNum = rest[:snEnd]
		}
	}
	return
}

func downloadCSV(filepath string) error {
	url := "https://storage.googleapis.com/play_public/supported_devices.csv"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func decodeUTF16(b []byte) (string, error) {
	if len(b)%2 != 0 {
		return "", fmt.Errorf("invalid UTF-16 length: must be even")
	}
	u16s := make([]uint16, len(b)/2)
	for i := 0; i < len(u16s); i++ {
		u16s[i] = uint16(b[2*i]) | (uint16(b[2*i+1]) << 8)
	}
	if len(u16s) > 0 && u16s[0] == 0xFEFF {
		u16s = u16s[1:]
	}
	return string(utf16.Decode(u16s)), nil
}

func getDeviceNameFromGooglePlay(modelCode string) string {
	csvFile := "supported_devices.csv"
	if _, err := os.Stat(csvFile); os.IsNotExist(err) {
		if err := downloadCSV(csvFile); err != nil {
			return ""
		}
	}
	f, err := os.Open(csvFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	content, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	var parsedContent string
	if len(content) > 2 && content[0] == 0xFF && content[1] == 0xFE {
		s, err := decodeUTF16(content)
		if err == nil {
			parsedContent = s
		}
	} else {
		parsedContent = string(content)
	}
	r := csv.NewReader(strings.NewReader(parsedContent))
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return ""
	}
	return findInRecords(records, modelCode)
}

func findInRecords(records [][]string, modelCode string) string {
	target := strings.ToLower(strings.TrimSpace(modelCode))
	for _, record := range records {
		if len(record) < 4 {
			continue
		}
		csvModel := strings.ToLower(strings.TrimSpace(record[3]))
		if csvModel == target {
			brand := record[0]
			marketingName := record[1]
			if marketingName == "" {
				return brand + " " + record[2]
			}
			return brand + " " + marketingName
		}
	}
	return ""
}

// --- Custom Theme ---

type myTheme struct{}

var _ fyne.Theme = (*myTheme)(nil)

func (m myTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if n == theme.ColorNameBackground {
		return color.RGBA{R: 15, G: 15, B: 16, A: 255}
	}
	return theme.DefaultTheme().Color(n, theme.VariantDark)
}

func (m myTheme) Font(s fyne.TextStyle) fyne.Resource {
	if customFont != nil {
		return customFont
	}
	return theme.DefaultTheme().Font(s)
}

func (m myTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(n)
}

func (m myTheme) Size(n fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(n)
}

// --- Custom Layout for Animation ---

type slideLayout interface {
	setOffsetY(v float32)
	fyne.Layout
}

type slideUpLayout struct {
	offsetY float32
}

func (l *slideUpLayout) setOffsetY(v float32) { l.offsetY = v }

func (l *slideUpLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, child := range objects {
		min := child.MinSize()
		x := (size.Width - min.Width) / 2
		y := (size.Height-min.Height)/2 + l.offsetY
		child.Move(fyne.NewPos(x, y))
		child.Resize(min)
	}
}

func (l *slideUpLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var min fyne.Size
	for _, child := range objects {
		cMin := child.MinSize()
		if cMin.Width > min.Width {
			min.Width = cMin.Width
		}
		if cMin.Height > min.Height {
			min.Height = cMin.Height
		}
	}
	return min
}

type fullSlideUpLayout struct {
	offsetY float32
}

func (l *fullSlideUpLayout) setOffsetY(v float32) { l.offsetY = v }

func (l *fullSlideUpLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, child := range objects {
		child.Move(fyne.NewPos(0, l.offsetY))
		child.Resize(size)
	}
}

func (l *fullSlideUpLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var min fyne.Size
	for _, child := range objects {
		cMin := child.MinSize()
		if cMin.Width > min.Width {
			min.Width = cMin.Width
		}
		if cMin.Height > min.Height {
			min.Height = cMin.Height
		}
	}
	return min
}

func triggerSlideUp(container *fyne.Container) {
	ly, ok := container.Layout.(slideLayout)
	if !ok {
		return
	}
	ly.setOffsetY(400)
	anim := fyne.NewAnimation(200*time.Millisecond, func(v float32) {
		ly.setOffsetY(400 * (1 - v))
		fyne.Do(func() {
			ly.Layout(container.Objects, container.Size())
			container.Refresh()
		})
	})
	anim.Curve = fyne.AnimationEaseOut
	anim.Start()
}

// --- UI Row Helper ---

func createInfoRow(title string, valLabel *widget.Label) *fyne.Container {
	titleObj := canvas.NewText(title, color.Gray{Y: 180})
	titleObj.TextSize = 12

	line := canvas.NewRectangle(color.RGBA{R: 0, G: 151, B: 178, A: 255})
	line.SetMinSize(fyne.NewSize(0, 2))

	valueWithLine := container.NewVBox(valLabel, line)
	compactValue := container.NewHBox(valueWithLine, layout.NewSpacer())

	rowContent := container.NewVBox(
		titleObj,
		compactValue,
		layout.NewSpacer(),
	)
	return rowContent
}

// --- USB Logic via gousb ---

// DeviceInfo cihazdan alınan kimlik bilgilerini taşır
type DeviceInfo struct {
	PID     string
	Mode    string
	Model   string
	Serial  string
	HasADB  bool
	HasTWRP bool
}

func detectConnectedDevice() *DeviceInfo {
	ctx := gousb.NewContext()
	defer ctx.Close()

	var foundDownload bool
	var foundRecovery bool

	devs, _ := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		vid := desc.Vendor
		pid := desc.Product.String()
		if vid == gousb.ID(0x04e8) && pid == "685d" {
			foundDownload = true
			return false
		}
		if vid == gousb.ID(0x04e8) && pid == "6860" {
			return true
		}
		if vid == gousb.ID(0x18d1) && pid == "4ee0" {
			return true
		}
		if vid == gousb.ID(0x18d1) && pid == "d001" {
			foundRecovery = true
			return true // aç, string oku, sonra kapat
		}
		if vid == gousb.ID(0x0000) && pid == "0000" {
			return true
		}
		return false
	})

	if foundDownload {
		for _, d := range devs { d.Close() }
		return &DeviceInfo{PID: "685d", Mode: "Download Mode"}
	}
	if len(devs) == 0 && !foundRecovery {
		return nil
	}

	var pid, model, serial string
	var mode string

	if len(devs) > 0 {
		dev := devs[0]
		pid = dev.Desc.Product.String()

		switch pid {
		case "4ee0":
			mode = "Fastboot Mode"
		case "d001":
			mode = "Recovery Mode"
		case "6860":
			hasADB := false
			for _, cfg := range dev.Desc.Configs {
				for _, intf := range cfg.Interfaces {
					for _, alt := range intf.AltSettings {
						// ADB Interface -> Class: 255 (0xFF), SubClass: 66 (0x42), Protocol: 1
						if alt.Class == gousb.ClassVendorSpec && alt.SubClass == 66 && alt.Protocol == 1 {
							hasADB = true
						}
					}
				}
			}
			if hasADB {
				mode = "Normal Mode (MTP + ADB)"
			} else {
				mode = "Normal Mode (MTP)"
			}
		case "0000":
			mfr, _ := dev.Manufacturer()
			if !strings.Contains(strings.ToUpper(mfr), "SAMSUNG") {
				for _, d := range devs { d.Close() }
				return nil
			}
			mode = "Booting..."
		default:
			mode = "Connected"
		}

		model, _ = dev.Product()
		serial, _ = dev.SerialNumber()

		// MTP/Boot modunda ADB'den model al
		if (pid == "6860" || pid == "0000") && (model == "SAMSUNG_Android" || model == "") {
			if _, err := exec.LookPath("adb"); err == nil {
				if out, err := exec.Command("adb", "shell", "getprop", "ro.product.model").Output(); err == nil {
					if m := strings.TrimSpace(string(out)); m != "" {
						model = m
					}
				}
			}
			// ADB boş döndüyse veya yoksa, MTP modunda AT komutu dene
			if pid == "6860" && (model == "SAMSUNG_Android" || model == "") {
				port := findSamsungModem()
				if port != "" {
					resp := sendATCommand(port, "AT+DEVCONINFO")
					atMdl, atSn := parseATDevConInfo(resp)
					if atMdl != "" {
						model = atMdl
					}
					if atSn != "" {
						serial = atSn
					}
				}
			}
		}

		// USB handle'ları şimdi kapat - ADB çakışmasını önle
		for _, d := range devs {
			d.Close()
		}
	}

	result := &DeviceInfo{PID: pid, Mode: mode, Model: model, Serial: serial}

	// Recovery modunda veya ADB destekli MTP modunda which twrp çalıştır
	if foundRecovery || (pid == "6860" && strings.Contains(mode, "ADB")) {
		whichOut, _ := exec.Command("adb", "shell", "which", "twrp").Output()
		if strings.TrimSpace(string(whichOut)) == "/system/bin/twrp" {
			result.HasTWRP = true
			result.Mode = "Recovery Mode (TWRP)"
		}
	}

	return result
}
// quickCheckConnectedPID: sadece PID string döndürür (state-machine için)
func quickCheckConnectedPID() string {
	info := detectConnectedDevice()
	if info == nil {
		return ""
	}
	return info.PID
}

func fetchDVIFData() string {
	ctx := gousb.NewContext()
	defer ctx.Close()

	var output strings.Builder

	devs, _ := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return desc.Vendor == gousb.ID(0x04e8) && desc.Product.String() == "685d"
	})

	if len(devs) == 0 {
		return ""
	}
	dev := devs[0]
	defer func() {
		for _, d := range devs {
			d.Close()
		}
	}()

	dev.SetAutoDetach(true)

	var epIn *gousb.InEndpoint
	var epOut *gousb.OutEndpoint
	var intf *gousb.Interface
	var config *gousb.Config

	for cfgNum, cfgDesc := range dev.Desc.Configs {
		cfg, err := dev.Config(cfgNum)
		if err != nil {
			continue
		}
		config = cfg

		for _, intfDesc := range cfgDesc.Interfaces {
			i, err := config.Interface(intfDesc.Number, 0)
			if err != nil {
				continue
			}

			var tempIn *gousb.InEndpoint
			var tempOut *gousb.OutEndpoint
			for _, epDesc := range intfDesc.AltSettings[0].Endpoints {
				if epDesc.TransferType == gousb.TransferTypeBulk {
					if epDesc.Direction == gousb.EndpointDirectionIn {
						tempIn, _ = i.InEndpoint(epDesc.Number)
					} else {
						tempOut, _ = i.OutEndpoint(epDesc.Number)
					}
				}
			}

			if tempIn != nil && tempOut != nil {
				epIn = tempIn
				epOut = tempOut
				intf = i
				break
			}
			i.Close()
		}

		if intf != nil {
			break // found and claimed interface
		}
		config.Close()
	}

	if intf != nil {
		defer intf.Close()
	}

	// Komutu gönder
	_, err := epOut.Write([]byte("DVIF"))
	if err != nil {
		return ""
	}

	// Yanıtı bekle
	buf := make([]byte, 16384)
	startFound := false
	for {
		n, err := epIn.Read(buf)
		if n > 0 {
			output.Write(buf[:n])
			strOut := output.String()

			if !startFound && strings.Contains(strOut, "@#") {
				startFound = true
			}

			if startFound {
				idx := strings.Index(strOut, "@#")
				if idx != -1 {
					afterStart := strOut[idx+2:]
					if strings.Contains(afterStart, "@#") || strings.Contains(afterStart, "#@") {
						return strOut
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return output.String()
}

// sendDeviceCommand: DVIF yerine herhangi bir komutu cihaza gönderir (ör: RESET)
func sendDeviceCommand(command string) {
	ctx := gousb.NewContext()
	defer ctx.Close()

	devs, _ := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return desc.Vendor == gousb.ID(0x04e8) && desc.Product.String() == "685d"
	})
	if len(devs) == 0 {
		return
	}
	dev := devs[0]
	defer func() {
		for _, d := range devs {
			d.Close()
		}
	}()
	dev.SetAutoDetach(true)

	var epOut *gousb.OutEndpoint
	var intf *gousb.Interface
	var config *gousb.Config

	for cfgNum, cfgDesc := range dev.Desc.Configs {
		cfg, err := dev.Config(cfgNum)
		if err != nil {
			continue
		}
		config = cfg
		for _, intfDesc := range cfgDesc.Interfaces {
			i, err := config.Interface(intfDesc.Number, 0)
			if err != nil {
				continue
			}
			for _, epDesc := range intfDesc.AltSettings[0].Endpoints {
				if epDesc.TransferType == gousb.TransferTypeBulk && epDesc.Direction == gousb.EndpointDirectionOut {
					epOut, _ = i.OutEndpoint(epDesc.Number)
				}
			}
			if epOut != nil {
				intf = i
				break
			}
			i.Close()
		}
		if intf != nil {
			break
		}
		config.Close()
	}
	if intf != nil {
		defer intf.Close()
	}
	if epOut != nil {
		epOut.Write([]byte(command)) //nolint
	}
}

// --- Main Application ---

const (
	StateDisconnected = iota
	StateConnecting
	StateConnected
)

type tappableObj struct {
	widget.BaseWidget
	content *fyne.Container
	onTap   func()
}

func newTappableObj(c *fyne.Container, onTap func()) *tappableObj {
	t := &tappableObj{content: c, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappableObj) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.content)
}

func (t *tappableObj) Tapped(_ *fyne.PointEvent) {
	if t.onTap != nil && runtime.GOOS != "windows" {
		t.onTap()
	}
}

func isCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func main() {
	myApp := app.NewWithID("com.galaxy.toolkit")
	myApp.Settings().SetTheme(&myTheme{})

	myWindow := myApp.NewWindow("Galaxy ToolKit")
	myWindow.Resize(fyne.NewSize(900, 600)) // Daha geniş pencere

	// --- Disconnected / Entry View ---
	entryImg := canvas.NewImageFromFile("images/connecting_start.png")
	entryImg.FillMode = canvas.ImageFillContain
	entryImg.SetMinSize(fyne.NewSize(250, 400))

	statusText := canvas.NewText("Please plug your Galaxy device", color.White)
	statusText.Alignment = fyne.TextAlignCenter
	statusText.TextSize = 18

	entryContent := container.NewVBox(
		entryImg,
		container.NewPadded(statusText),
	)

	// Özel layout ile animasyon ekranı oluşturuyoruz
	customLayout := &slideUpLayout{offsetY: 400}
	disconnectedContainer := container.New(customLayout, entryContent)

	// --- Connected View (Border) ---
	connectedImg := canvas.NewImageFromFile("images/basic_device.png")
	connectedImg.FillMode = canvas.ImageFillContain
	connectedImg.SetMinSize(fyne.NewSize(300, 450))

	// Bilgi Etiketleri
	modelLabel := widget.NewLabel("-")
	modelLabel.TextStyle = fyne.TextStyle{Bold: true}

	productLabel := widget.NewLabel("-")
	productLabel.TextStyle = fyne.TextStyle{Bold: true}

	vendorLabel := widget.NewLabel("-")
	vendorLabel.TextStyle = fyne.TextStyle{Bold: true}

	fwVerLabel := widget.NewLabel("-")
	fwVerLabel.TextStyle = fyne.TextStyle{Bold: true}

	capaLabel := widget.NewLabel("-")
	capaLabel.TextStyle = fyne.TextStyle{Bold: true}

	didLabel := widget.NewLabel("-")
	didLabel.TextStyle = fyne.TextStyle{Bold: true}

	detailsList := container.NewVBox(
		createInfoRow("Model Name", modelLabel),
		createInfoRow("Product Name", productLabel),
		createInfoRow("Vendor (Carrier)", vendorLabel),
		createInfoRow("Firmware Version", fwVerLabel),
		createInfoRow("Storage Capacity", capaLabel),
		createInfoRow("Device ID", didLabel),
	)

// --- Odin Flash Area ---

	// Path truncation helper: "~/lengthy/path/to/file.tar" → "/home/user/len...ile.tar"
	truncateMiddle := func(s string, maxLen int) string {
		r := []rune(s)
		if len(r) <= maxLen {
			return s
		}
		keep := (maxLen - 3) / 2
		return string(r[:keep]) + "..." + string(r[len(r)-keep:])
	}

	// Native file picker: kdialog → zenity → fyne fallback
	openNativePicker := func(onPicked func(path string)) {
		if runtime.GOOS == "windows" {
			return
		}
		filter := "*.tar *.tar.md5 *.md5 *.bin *.lz4 *.img"
		var cmd *exec.Cmd
		switch {
		case isCommandAvailable("kdialog"):
			cmd = exec.Command("kdialog", "--getopenfilename", ".", filter)
		case isCommandAvailable("zenity"):
			cmd = exec.Command("zenity", "--file-selection",
				"--file-filter=Samsung Firmware|*.tar *.tar.md5 *.md5 *.bin *.lz4 *.img",
				"--title=Select firmware file")
		default:
			cmd = nil
		}
		if cmd != nil {
			out, err := cmd.Output()
			if err == nil {
				p := strings.TrimSpace(string(out))
				if p != "" {
					onPicked(p)
				}
			}
			return
		}
		// Fallback: Fyne dialog (rare)
		dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err == nil && reader != nil {
				onPicked(reader.URI().Path())
			}
		}, myWindow).Show()
	}

	// createPartitionRow: DVIF tarzı altı mavi çizgili, tıklanabilir, başlık + yol satırı
	type partitionRow struct {
		fullPath string
		pathLabel *widget.Label
	}
	var (
		apPart   = &partitionRow{}
		blPart   = &partitionRow{}
		cpPart   = &partitionRow{}
		cscPart  = &partitionRow{}
		homePart = &partitionRow{}
	)

	createPartitionRow := func(title string, part *partitionRow) *fyne.Container {
		titleObj := canvas.NewText(title, color.Gray{Y: 180})
		titleObj.TextSize = 12

		part.pathLabel = widget.NewLabel("Click to select...")
		part.pathLabel.TextStyle = fyne.TextStyle{Bold: true}

		line := canvas.NewRectangle(color.RGBA{R: 0, G: 151, B: 178, A: 255})
		line.SetMinSize(fyne.NewSize(0, 2))

		valueWithLine := container.NewVBox(part.pathLabel, line)

		// Clear butonu - sadece dosya seçilince görünür
		clearBtn := widget.NewButton("×", nil)
		clearBtn.Importance = widget.LowImportance
		clearBtn.Hide()
		clearBtn.OnTapped = func() {
			part.fullPath = ""
			part.pathLabel.SetText("Click to select...")
			clearBtn.Hide()
		}

		rowContent := container.NewVBox(
			titleObj,
			container.NewBorder(nil, nil, nil, clearBtn, valueWithLine),
		)

		tapTarget := newTappableObj(rowContent, func() {
			openNativePicker(func(p string) {
				part.fullPath = p
				fyne.Do(func() {
					part.pathLabel.SetText(truncateMiddle(p, 32))
					clearBtn.Show()
				})
			})
		})
		return container.NewPadded(tapTarget)
	}

	apRow := createPartitionRow("AP", apPart)
	blRow := createPartitionRow("BL", blPart)
	cpRow := createPartitionRow("CP", cpPart)
	cscRow := createPartitionRow("CSC", cscPart)
	homeRow := createPartitionRow("HOME", homePart)

	nandEraseCheck := widget.NewCheck("Nand Erase", nil)

	// Terminal - mavi kenarlıklı ve scroll destekli
	terminalOutput := widget.NewMultiLineEntry()
	terminalOutput.Disable()
	termBorder := canvas.NewRectangle(color.Transparent)
	termBorder.StrokeColor = color.RGBA{R: 0, G: 151, B: 178, A: 255}
	termBorder.StrokeWidth = 2
	termBorder.CornerRadius = 3
	termBox := container.NewMax(termBorder, container.NewScroll(terminalOutput))

	// Flash butonu - aynı mavi kenarlıklı kutucuk tasarımı, Tappable
	flashLabelText := canvas.NewText("FLASH", color.White)
	flashLabelText.TextStyle = fyne.TextStyle{Bold: true}
	flashLabelText.TextSize = 14

	flashBorder := canvas.NewRectangle(color.Transparent)
	flashBorder.StrokeColor = color.RGBA{R: 0, G: 151, B: 178, A: 255}
	flashBorder.StrokeWidth = 2
	flashBorder.CornerRadius = 3

	flashContent := container.NewCenter(flashLabelText)
	flashVisuals := container.NewMax(flashBorder, flashContent)

	flashAction := func() {
		if runtime.GOOS == "windows" {
			return
		}
		var args []string
		if apPart.fullPath != ""   { args = append(args, "-a", apPart.fullPath) }
		if blPart.fullPath != ""   { args = append(args, "-b", blPart.fullPath) }
		if cpPart.fullPath != ""   { args = append(args, "-c", cpPart.fullPath) }
		if cscPart.fullPath != ""  { args = append(args, "-s", cscPart.fullPath) }
		if homePart.fullPath != "" { args = append(args, "-u", homePart.fullPath) }
		if nandEraseCheck.Checked  { args = append(args, "-e") }

		if len(args) == 0 {
			terminalOutput.SetText("Error: No files selected to flash\n")
			return
		}
		terminalOutput.SetText("Starting odin4...\n")
		go func() {
			cmd := exec.Command("odin4", args...)
			stdout, _ := cmd.StdoutPipe()
			stderr, _ := cmd.StderrPipe()
			if err := cmd.Start(); err != nil {
				fyne.Do(func() {
					terminalOutput.SetText(terminalOutput.Text + "Error: " + err.Error() + "\n")
				})
				return
			}
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					fyne.Do(func() { terminalOutput.SetText(terminalOutput.Text + line + "\n") })
				}
			}()
			go func() {
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					line := scanner.Text()
					fyne.Do(func() { terminalOutput.SetText(terminalOutput.Text + line + "\n") })
				}
			}()
			cmd.Wait()
			fyne.Do(func() { terminalOutput.SetText(terminalOutput.Text + "\nFlash finished.\n") })
		}()
	}

	flashTappable := newTappableObj(flashVisuals, flashAction)

	if runtime.GOOS == "windows" {
		nandEraseCheck.Disable()
		terminalOutput.SetText("odin4 is not supported on Windows. Please use original Odin.")
	}

	// Shutdown Device
	shutdownLabelText := canvas.NewText("SHUTDOWN", color.RGBA{R: 200, G: 60, B: 60, A: 255})
	shutdownLabelText.TextStyle = fyne.TextStyle{Bold: true}
	shutdownLabelText.TextSize = 12
	shutdownBorder := canvas.NewRectangle(color.Transparent)
	shutdownBorder.StrokeColor = color.RGBA{R: 200, G: 60, B: 60, A: 255}
	shutdownBorder.StrokeWidth = 2
	shutdownBorder.CornerRadius = 3
	shutdownVisuals := container.NewMax(shutdownBorder, container.NewCenter(shutdownLabelText))
	shutdownTappable := newTappableObj(shutdownVisuals, func() {
		go sendDeviceCommand("RESET")
	})

	// Reboot Device
	rebootLabelText := canvas.NewText("REBOOT", color.RGBA{R: 220, G: 140, B: 0, A: 255})
	rebootLabelText.TextStyle = fyne.TextStyle{Bold: true}
	rebootLabelText.TextSize = 12
	rebootBorder := canvas.NewRectangle(color.Transparent)
	rebootBorder.StrokeColor = color.RGBA{R: 220, G: 140, B: 0, A: 255}
	rebootBorder.StrokeWidth = 2
	rebootBorder.CornerRadius = 3
	rebootVisuals := container.NewMax(rebootBorder, container.NewCenter(rebootLabelText))
	rebootTappable := newTappableObj(rebootVisuals, func() {
		if runtime.GOOS != "windows" {
			go exec.Command("odin4", "--reboot").Run() //nolint
		}
	})

	rightPanel := container.NewBorder(
		container.NewVBox(apRow, blRow, cpRow, cscRow, homeRow,
			container.NewPadded(nandEraseCheck)),
		container.NewPadded(container.NewGridWithColumns(3,
			container.NewPadded(flashTappable),
			container.NewPadded(rebootTappable),
			container.NewPadded(shutdownTappable),
		)),
		nil, nil,
		container.NewPadded(termBox),
	)

	centerArea := container.NewGridWithColumns(2,
		container.NewPadded(container.NewVBox(layout.NewSpacer(), detailsList, layout.NewSpacer())),
		rightPanel,
	)

	connectedContainer := container.NewPadded(centerArea)
	connectedLayout := &fullSlideUpLayout{offsetY: 400}
	connectedContainerWrapper := container.New(connectedLayout, connectedContainer)

	// --- Normal Mode UI (Fastboot / Recovery / MTP) ---
	nModeLabel := widget.NewLabel("-")
	nModeLabel.TextStyle = fyne.TextStyle{Bold: true}
	nModelLabel := widget.NewLabel("-")
	nModelLabel.TextStyle = fyne.TextStyle{Bold: true}
	nSerialLabel := widget.NewLabel("-")
	nSerialLabel.TextStyle = fyne.TextStyle{Bold: true}

	normalInfoList := container.NewVBox(
		createInfoRow("Device Mode", nModeLabel),
		createInfoRow("Model", nModelLabel),
		createInfoRow("Serial Number", nSerialLabel),
	)

	// --- Normal Mode Terminal ---
	normalLog := widget.NewMultiLineEntry()
	normalLog.Disable()
	normalLog.SetText("Normal Mode Logs Ready.\n")
	nLogBorder := canvas.NewRectangle(color.Transparent)
	nLogBorder.StrokeColor = color.RGBA{R: 0, G: 151, B: 178, A: 255}
	nLogBorder.StrokeWidth = 2
	nLogBorder.CornerRadius = 3
	nLogBox := container.NewMax(nLogBorder, container.NewScroll(normalLog))

	appendNormalLog := func(line string) {
		fyne.Do(func() {
			normalLog.SetText(normalLog.Text + line + "\n")
		})
	}

	// --- Normal Mode AT Buttons ---
	createNormalButton := func(title string, onTap func()) fyne.CanvasObject {
		txt := canvas.NewText(title, color.White)
		txt.TextStyle = fyne.TextStyle{Bold: true}
		txt.TextSize = 13
		line := canvas.NewRectangle(color.RGBA{R: 0, G: 151, B: 178, A: 255})
		line.SetMinSize(fyne.NewSize(0, 2))
		cell := container.NewVBox(txt, line)
		tap := newTappableObj(cell, onTap)
		return container.NewPadded(tap)
	}

	download1Btn := createNormalButton("Download Mode", func() {
		appendNormalLog("[SEND] AT+FUS?")
		port := findSamsungModem()
		if port != "" {
			resp := sendATCommand(port, "AT+FUS?")
			appendNormalLog("[RESP] " + strings.TrimSpace(resp))
		} else {
			appendNormalLog("[ERROR] Modem not found!")
		}
	})
	download2Btn := createNormalButton("Download Mode 2", func() {
		appendNormalLog("[SEND] AT+SUDDLMOD=0,0")
		port := findSamsungModem()
		if port != "" {
			resp := sendATCommand(port, "AT+SUDDLMOD=0,0")
			appendNormalLog("[RESP] " + strings.TrimSpace(resp))
		} else {
			appendNormalLog("[ERROR] Modem not found!")
		}
	})
	normalRebootBtn := createNormalButton("Reboot Device", func() {
		appendNormalLog("[SEND] AT+CFUN=1,1")
		port := findSamsungModem()
		if port != "" {
			resp := sendATCommand(port, "AT+CFUN=1,1")
			appendNormalLog("[RESP] " + strings.TrimSpace(resp))
		} else {
			appendNormalLog("[ERROR] Modem not found!")
		}
	})

	normalButtons := container.NewGridWithColumns(3, download1Btn, download2Btn, normalRebootBtn)

	normalRightPanel := container.NewBorder(
		nil,
		container.NewPadded(normalButtons),
		nil, nil,
		container.NewPadded(nLogBox),
	)

	normalLayout := container.NewGridWithColumns(2,
		container.NewPadded(container.NewVBox(layout.NewSpacer(), normalInfoList, layout.NewSpacer())),
		normalRightPanel,
	)

	normalArea := container.NewPadded(normalLayout)
	normalConnectedLayout := &fullSlideUpLayout{offsetY: 400}
	normalContainerWrapper := container.New(normalConnectedLayout, normalArea)

	// --- TWRP UI ---
	twrpModeLabel := widget.NewLabel("-")
	twrpModeLabel.TextStyle = fyne.TextStyle{Bold: true}
	twrpModelLabel := widget.NewLabel("-")
	twrpModelLabel.TextStyle = fyne.TextStyle{Bold: true}
	twrpSerialLabel := widget.NewLabel("-")
	twrpSerialLabel.TextStyle = fyne.TextStyle{Bold: true}

	twrpInfo := container.NewVBox(
		createInfoRow("Device Mode", twrpModeLabel),
		createInfoRow("Model", twrpModelLabel),
		createInfoRow("Serial Number", twrpSerialLabel),
	)

	// TWRP hücre yardımcısı: Install tarzı altı mavi çizgili Tappable düğme
	createTWRPCell := func(title string, active bool, onTap func()) fyne.CanvasObject {
		txt := canvas.NewText(title, color.White)
		txt.TextStyle = fyne.TextStyle{Bold: true}
		txt.TextSize = 14
		if !active {
			txt.Color = color.RGBA{R: 80, G: 80, B: 80, A: 255}
		}
		line := canvas.NewRectangle(color.RGBA{R: 0, G: 151, B: 178, A: 255})
		if !active {
			line.FillColor = color.RGBA{R: 40, G: 60, B: 70, A: 255}
		}
		line.SetMinSize(fyne.NewSize(0, 2))
		cell := container.NewVBox(txt, line)
		if active && onTap != nil {
			tap := newTappableObj(cell, onTap)
			return container.NewPadded(tap)
		}
		return container.NewPadded(cell)
	}

	// Install dosya tarayıcı state
	twrpCurrentPath := "/sdcard/"
	_ = twrpCurrentPath
	installFileList := container.NewVBox()

	// Geri tuşu oluşturucu
	createBackButton := func(onTap func()) fyne.CanvasObject {
		btnTxt := canvas.NewText("< Back", color.White)
		btnTxt.TextStyle = fyne.TextStyle{Bold: true}
		btnTxt.TextSize = 13
		btnBg := canvas.NewRectangle(color.RGBA{R: 30, G: 50, B: 60, A: 255})
		btnBg.CornerRadius = 3
		btnBg.SetMinSize(fyne.NewSize(0, 30))
		btnContent := container.NewMax(btnBg, container.NewCenter(btnTxt))
		return newTappableObj(btnContent, onTap)
	}

	// Log alanı
	installLog := widget.NewMultiLineEntry()
	installLog.Disable()
	installLog.SetText("Ready. Select a file to install.\n")
	installLog.SetMinRowsVisible(20)
	installLogBorder := canvas.NewRectangle(color.Transparent)
	installLogBorder.StrokeColor = color.RGBA{R: 0, G: 151, B: 178, A: 255}
	installLogBorder.StrokeWidth = 2
	installLogBorder.CornerRadius = 3
	installLogScroll := container.NewScroll(installLog)
	installLogBox := container.NewMax(installLogBorder, installLogScroll)

	appendInstallLog := func(line string) {
		fyne.Do(func() {
			installLog.SetText(installLog.Text + line + "\n")
			// en alta scroll'lamak otomatik olmadığı için Fyne'da bazen cursor set ediliyor ama biz şimdilik sadece ekliyoruz
		})
	}

	runAdbLogCmd := func(cmdArgs ...string) {
		cmd := exec.Command("adb", cmdArgs...)
		cmd.Stdout = nil
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = nil
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			appendInstallLog("[ERROR] " + err.Error())
			return
		}
		go func() { sc := bufio.NewScanner(stdout); for sc.Scan() { appendInstallLog(sc.Text()) } }()
		go func() { sc := bufio.NewScanner(stderr); for sc.Scan() { appendInstallLog("[ERR] " + sc.Text()) } }()
		cmd.Wait()
	}

	// TWRP görünümleri
	twrpGridContainer := container.NewVBox() // Grid
	
	// Dosya penceresi (sadece dosya listesi ve Geri tuşu)
	fileExplorerBackBtn := createBackButton(func() {
		// installView'ı gizle, grid'i göster
		// Buradaki "installView" henüz tanımlanmadı ama variable hoisting var
	})
	installView := container.NewBorder(nil, container.NewPadded(fileExplorerBackBtn), nil, nil, container.NewScroll(installFileList))
	installView.Hide()

	// Log penceresi (sadece log ve kurulum bitince Geri tuşu)
	logViewBackBtnWrapper := container.NewVBox() // Geri tuşu için gizlenebilir taşıyıcı
	logViewBackBtn := createBackButton(func() {
		// Log view'dan twrp grid'e dön
	})
	logViewBackBtnWrapper.Add(container.NewPadded(logViewBackBtn))
	logViewBackBtnWrapper.Hide() // başlangıçta gizli
	logView := container.NewBorder(nil, logViewBackBtnWrapper, nil, nil, container.NewPadded(installLogBox))
	logView.Hide()

	// Handler'ları fonksiyona bağla
	fileExplorerBackBtn.(*tappableObj).onTap = func() {
		installView.Hide()
		twrpGridContainer.Show()
	}
	logViewBackBtn.(*tappableObj).onTap = func() {
		logView.Hide()
		twrpGridContainer.Show()
	}

	// Reboot penceresi
	rebootBackBtn := createBackButton(func() {})
	rebootGrid := container.NewGridWithColumns(2,
		createTWRPCell("System", true, func() { go exec.Command("adb", "reboot").Run() }),
		createTWRPCell("Power Off", true, func() { go exec.Command("adb", "shell", "reboot", "-p").Run() }),
		createTWRPCell("Recovery", true, func() { go exec.Command("adb", "reboot", "recovery").Run() }),
		createTWRPCell("Download", true, func() { go exec.Command("adb", "reboot", "download").Run() }),
		createTWRPCell("Bootloader", true, func() { go exec.Command("adb", "reboot", "bootloader").Run() }),
		createTWRPCell("Fastboot", true, func() { go exec.Command("adb", "reboot", "fastboot").Run() }),
	)
	rebootGridContainer := container.NewVBox(
		layout.NewSpacer(),
		rebootGrid,
		layout.NewSpacer(),
	)
	rebootView := container.NewBorder(nil, container.NewPadded(rebootBackBtn), nil, nil, container.NewPadded(rebootGridContainer))
	rebootView.Hide()

	rebootBackBtn.(*tappableObj).onTap = func() {
		rebootView.Hide()
		twrpGridContainer.Show()
	}

	// Çok satırlı uyarı metni oluşturucu
	makeMultilineText := func(content string) *fyne.Container {
		vb := container.NewVBox()
		for _, line := range strings.Split(content, "\n") {
			txt := canvas.NewText(line, color.White)
			txt.Alignment = fyne.TextAlignCenter
			vb.Add(txt)
		}
		return container.NewCenter(vb)
	}

	// Format Data Penceresi
	formatText := makeMultilineText("Format Data will wipe all of your apps, backups, pictures,\nvideos, media and removes encryption on internal storage.\n\nThis cannot be undone.\nType yes to continue. Press back to cancel.")
	formatEntry := widget.NewEntry()
	formatEntry.PlaceHolder = "yes"
	var formatDataView *fyne.Container // ön tanım
	formatSubmit := widget.NewButton("Confirm", func() {
		if strings.ToLower(strings.TrimSpace(formatEntry.Text)) == "yes" {
			formatDataView.Hide()
			logView.Show()
			logViewBackBtnWrapper.Hide()
			installLog.SetText("Formatting Data...\n")
			go func() {
				runAdbLogCmd("shell", "-t", "-t", "twrp", "format", "data")
				appendInstallLog("[DONE] Format Data complete.")
				fyne.Do(func() { logViewBackBtnWrapper.Show() })
			}()
		}
	})
	formatBackBtn := createBackButton(func() {})
	formatBottom := container.NewVBox(formatSubmit, layout.NewSpacer(), formatBackBtn)
	formatDataView = container.NewBorder(nil, container.NewPadded(formatBottom), nil, nil, container.NewVBox(layout.NewSpacer(), formatText, formatEntry, layout.NewSpacer()))
	formatDataView.Hide()

	// Wipe (Factory Reset) Penceresi
	wipeText := makeMultilineText("Wipes Data, Cache, and Dalvik\n(not including internal storage)\n\nMost of the time this is the only wipe that you need.\n\nPress back button to cancel it")
	var wipeView *fyne.Container // ön tanım
	formatDataBtn := createTWRPCell("Format Data", true, func() {
		wipeView.Hide()
		formatEntry.SetText("") // resetle
		formatDataView.Show()
	})
	advancedWipeBtn := createTWRPCell("Advanced Wipe", false, nil)
	wipeTypeGrid := container.NewGridWithColumns(2, advancedWipeBtn, formatDataBtn)

	wipeTriggered := false
	wipeSlider := widget.NewSlider(0, 100)
	swipeText := canvas.NewText("Swipe to Factory Reset", color.Gray{Y: 150})
	swipeText.Alignment = fyne.TextAlignCenter
	sliderArea := container.NewVBox(swipeText, wipeSlider)

	wipeSlider.OnChanged = func(val float64) {
		if val >= 100 && !wipeTriggered {
			wipeTriggered = true
			wipeView.Hide()
			logView.Show()
			logViewBackBtnWrapper.Hide()
			installLog.SetText("Starting Factory Reset...\n")

			go func() {
				runAdbLogCmd("shell", "-t", "-t", "twrp", "wipe", "data")
				runAdbLogCmd("shell", "-t", "-t", "twrp", "wipe", "cache")
				runAdbLogCmd("shell", "-t", "-t", "twrp", "wipe", "dalvik")
				appendInstallLog("[DONE] Wipe complete.")
				fyne.Do(func() { logViewBackBtnWrapper.Show() })
			}()
		}
	}

	wipeBackBtn := createBackButton(func() {
		wipeView.Hide()
		twrpGridContainer.Show()
	})
	formatBackBtn.(*tappableObj).onTap = func() {
		formatDataView.Hide()
		wipeView.Show()
	}

	wipeBottom := container.NewVBox(sliderArea, layout.NewSpacer(), wipeBackBtn)
	wipeView = container.NewBorder(wipeTypeGrid, container.NewPadded(wipeBottom), nil, nil, container.NewCenter(wipeText))
	wipeView.Hide()

	twrpRightStack := container.NewMax(twrpGridContainer, installView, logView, rebootView, wipeView, formatDataView)

	// Dosya listesini yenileyen fonksiyon
	var refreshInstallList func(path string)
	refreshInstallList = func(path string) {
		twrpCurrentPath = path
		out, err := exec.Command("adb", "shell", "ls", "-p", path).Output()
		var entries []string
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					entries = append(entries, line)
				}
			}
		}

		fyne.Do(func() {
			installFileList.Objects = nil

			// "(Up A Level)" butonu
		upLabel := canvas.NewText("📂  (Up A Level)", color.RGBA{R: 0, G: 200, B: 220, A: 255})
		upLabel.TextSize = 13
		upLine := canvas.NewRectangle(color.RGBA{R: 0, G: 80, B: 100, A: 255})
		upLine.SetMinSize(fyne.NewSize(0, 1))
		upRow := container.NewVBox(upLabel, upLine)
		upTap := newTappableObj(upRow, func() {
			if path == "/sdcard/" || path == "/sdcard" {
				go refreshInstallList("/")
			} else {
				parts := strings.TrimRight(path, "/")
				parent := parts[:strings.LastIndex(parts, "/")+1]
				if parent == "" {
					parent = "/"
				}
				go refreshInstallList(parent)
			}
		})
		installFileList.Add(container.NewPadded(upTap))

			for _, entry := range entries {
				entryCapture := entry
				isDir := strings.HasSuffix(entryCapture, "/")
				name := strings.TrimSuffix(entryCapture, "/")
				var icon fyne.Resource
				if isDir {
					icon = theme.FolderIcon()
				} else {
					icon = theme.DocumentIcon()
				}
				iconImg := widget.NewIcon(icon)
				nameLbl := canvas.NewText(name, color.White)
				nameLbl.TextSize = 13
				separator := canvas.NewRectangle(color.RGBA{R: 30, G: 50, B: 60, A: 255})
				separator.SetMinSize(fyne.NewSize(0, 1))
				rowContent := container.NewBorder(nil, separator, container.NewPadded(iconImg), nil,
					container.NewPadded(nameLbl))
				rowTap := newTappableObj(rowContent, func() {
					if isDir {
						newPath := strings.TrimRight(path, "/") + "/" + name + "/"
						go refreshInstallList(newPath)
					} else {
						if !strings.HasSuffix(strings.ToLower(name), ".zip") {
							// Geçersiz dosya uyarı (log ekranına geç, saniyelik uyarı göster)
							fyne.Do(func() {
								installView.Hide()
								logView.Show()
								logViewBackBtnWrapper.Show()
								installLog.SetText("Ready. Select a file to install.\n")
								appendInstallLog("[ERROR] " + name + " is not a ZIP file, it can not be installable.")
							})
						} else {
							// ZIP kur
							fullZipPath := strings.TrimRight(path, "/") + "/" + name
							fyne.Do(func() {
								installView.Hide()
								logView.Show()
								logViewBackBtnWrapper.Hide() // kurulum bitene kadar gizli
								installLog.SetText("Ready. Select a file to install.\n")
								appendInstallLog("[INSTALL] " + name)
							})
							
							go func() {
								runAdbLogCmd("shell", "-t", "-t", "twrp", "install", fullZipPath)
								appendInstallLog("[DONE] Install finished.")
								fyne.Do(func() { logViewBackBtnWrapper.Show() }) // Bitti, butonu göster
							}()
						}
					}
				})
				installFileList.Add(container.NewPadded(rowTap))
			}
			installFileList.Refresh()
		})
	}

	// TWRP Grid'ini oluştur
	installCellContainer := createTWRPCell("Install", true, func() {
		twrpGridContainer.Hide()
		installView.Show()
		go refreshInstallList("/sdcard/")
	})
	
	rebootCellContainer := createTWRPCell("Reboot", true, func() {
		twrpGridContainer.Hide()
		rebootView.Show()
	})
	
	wipeCellContainer := createTWRPCell("Wipe", true, func() {
		twrpGridContainer.Hide()
		wipeTriggered = false
		wipeSlider.SetValue(0)
		wipeView.Show()
	})
	
	twrpGrid := container.NewGridWithColumns(2,
		installCellContainer,
		wipeCellContainer,
		createTWRPCell("Backup", false, nil),
		createTWRPCell("Restore", false, nil),
		createTWRPCell("Mount", false, nil),
		createTWRPCell("Settings", false, nil),
		createTWRPCell("Advanced", false, nil),
		rebootCellContainer,
	)
	
	twrpGridContainer.Objects = []fyne.CanvasObject{
		layout.NewSpacer(),
		twrpGrid,
		layout.NewSpacer(),
	}



	twrpLayout := container.NewGridWithColumns(2,
		container.NewPadded(container.NewVBox(layout.NewSpacer(), twrpInfo, layout.NewSpacer())),
		container.NewPadded(twrpRightStack),
	)
	twrpConnectedLayout := &fullSlideUpLayout{offsetY: 400}
	twrpContainerWrapper := container.New(twrpConnectedLayout, container.NewPadded(twrpLayout))

	connectedContainerWrapper.Hide()
	normalContainerWrapper.Hide()
	twrpContainerWrapper.Hide()
	disconnectedContainer.Show()

	rootContainer := container.NewMax(disconnectedContainer, connectedContainerWrapper, normalContainerWrapper, twrpContainerWrapper)
	myWindow.SetContent(rootContainer)

	state := StateDisconnected
	var connectingTime time.Time

	// Animasyonu başlat (Açılışta hızlı aşağıdan yukarı çıkış)
	go func() {
		triggerSlideUp(disconnectedContainer)
	}()

	// Auto-Detection Loop
	var lastDevInfo *DeviceInfo
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		for range ticker.C {
			devInfo := detectConnectedDevice()
			pid := ""
			if devInfo != nil {
				pid = devInfo.PID
			}

			switch state {
			case StateDisconnected:
				if pid != "" {
					state = StateConnecting
					connectingTime = time.Now()

					fyne.Do(func() {
						entryImg.File = "images/pc_phone.png"
						entryImg.Refresh()

						statusText.Text = "Connecting to the Galaxy device..."
						statusText.Refresh()
						triggerSlideUp(disconnectedContainer) // Resim değişince animasyon çalsın
					})
				}

			case StateConnecting:
				if pid == "" {
					state = StateDisconnected
					fyne.Do(func() {
						entryImg.File = "images/connecting_start.png"
						entryImg.Refresh()

						statusText.Text = "Please plug your Galaxy device"
						statusText.Refresh()
						triggerSlideUp(disconnectedContainer) // İptal olunca animasyon çalsın
					})
				} else if time.Since(connectingTime) >= 1*time.Second {
					state = StateConnected
					lastDevInfo = devInfo

					if pid == "685d" {
						// Download modu - DVIF ile veri çek
						dataStr := fetchDVIFData()
						ymirData := parseYmirOutput(dataStr)

						if len(ymirData) > 0 {
							commercialName := getDeviceNameFromGooglePlay(ymirData["MODEL"])
							displayModel := ymirData["MODEL"]
							if commercialName != "" {
								displayModel = commercialName + " (" + displayModel + ")"
							}
							capa := ymirData["CAPA"]
							if capa != "" {
								capa += " GB"
							} else {
								capa = "Unknown"
							}
							fyne.Do(func() {
								modelLabel.SetText(displayModel)
								productLabel.SetText(ymirData["PRODUCT"])
								vendorLabel.SetText(ymirData["VENDOR"])
								fwVerLabel.SetText(ymirData["FWVER"])
								capaLabel.SetText(capa)
								didLabel.SetText(ymirData["DID"])
							})
						} else {
							fyne.Do(func() {
								modelLabel.SetText("Unknown")
								productLabel.SetText("-")
								vendorLabel.SetText("-")
								fwVerLabel.SetText("-")
								capaLabel.SetText("-")
								didLabel.SetText("-")
							})
						}
						fyne.Do(func() {
							disconnectedContainer.Hide()
							connectedContainerWrapper.Show()
							triggerSlideUp(connectedContainerWrapper)
						})
					} else {
						// Normal / Fastboot / Recovery / Boot modu
						var dMode, dModel, dSerial string
						var dHasTWRP bool
						if devInfo != nil {
							dMode = devInfo.Mode
							dModel = devInfo.Model
							dSerial = devInfo.Serial
							dHasTWRP = devInfo.HasTWRP
						}
						if dMode == "" { dMode = "-" }
						if dModel == "" { dModel = "-" }
						if dSerial == "" { dSerial = "-" }

						if dHasTWRP {
							fyne.Do(func() {
								twrpModeLabel.SetText(dMode)
								twrpModelLabel.SetText(dModel)
								twrpSerialLabel.SetText(dSerial)
								// Grid'e geri dön
								installView.Hide()
								logView.Hide()
								rebootView.Hide()
								wipeView.Hide()
								formatDataView.Hide()
								twrpGridContainer.Show()
								disconnectedContainer.Hide()
								normalContainerWrapper.Hide()
								twrpContainerWrapper.Show()
								triggerSlideUp(twrpContainerWrapper)
							})
						} else {
							fyne.Do(func() {
								nModeLabel.SetText(dMode)
								nModelLabel.SetText(dModel)
								nSerialLabel.SetText(dSerial)
								disconnectedContainer.Hide()
								normalContainerWrapper.Show()
								triggerSlideUp(normalContainerWrapper)
							})
						}
					}
				}

			case StateConnected:
				if pid == "" {
					state = StateDisconnected
					lastDevInfo = nil
					fyne.Do(func() {
						connectedContainerWrapper.Hide()
						normalContainerWrapper.Hide()
						twrpContainerWrapper.Hide()
						installView.Hide()
						logView.Hide()
						rebootView.Hide()
						wipeView.Hide()
						formatDataView.Hide()
						twrpGridContainer.Show() // baştan grid açık olsun
						disconnectedContainer.Show()
						entryImg.File = "images/connecting_start.png"
						entryImg.Refresh()
						statusText.Text = "Please plug your Galaxy device"
						statusText.Refresh()
						triggerSlideUp(disconnectedContainer)
					})
				}
				_ = lastDevInfo
			}
		}
	}()

	myWindow.ShowAndRun()
}
