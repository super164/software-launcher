package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows/registry"
)

// debugLog 把诊断信息写到 %APPDATA%/Local/AppStarter/debug.log，
// 便于排查「配置存在但 UI 显示为空」这类问题。失败时静默忽略。
// 日志上限 256KB，超出后清空重写，避免长期运行无限增长。
func debugLog(format string, args ...interface{}) {
	const maxSize = 256 << 10
	dir := filepath.Join(os.Getenv("APPDATA"), "Local", "AppStarter")
	_ = os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, "debug.log")

	flag := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSize {
		flag = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	}
	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// AppInfo 前端可见的软件记录：名称 + 路径 + 图标（PNG data URL）。
type AppInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Icon string `json:"icon"`
}

// Config 落盘结构：手动区 / 自动抓取区 + 两个开关。
type Config struct {
	Manual      []AppInfo `json:"manual"`
	Captured    []AppInfo `json:"captured"`
	AutoStart   bool      `json:"auto_start"`
	AutoRestore bool      `json:"auto_restore"`
}

// App 是 Wails 绑定的后端对象。
type App struct {
	ctx context.Context
	cfg Config
	mu  sync.Mutex
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.cfg = loadConfig()
	syncAutoStart(a.cfg.AutoStart)
	if a.cfg.AutoRestore {
		go func() {
			time.Sleep(8 * time.Second)
			a.LaunchAll()
		}()
	}
}

func (a *App) shutdown(ctx context.Context) {
	// 关闭时静默保存一次，避免用户忘记点保存。
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = saveConfig(a.cfg)
}

// ============ 配置读写 ============

func getConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "AppData", "Local", "AppStarter", "apps.json")
}

func loadConfig() Config {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return Config{}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}
	}
	// 图标是运行时产物，重新提取（配置里可能是空的）。
	for i := range cfg.Manual {
		cfg.Manual[i].Icon = extractIconDataURL(cfg.Manual[i].Path)
	}
	for i := range cfg.Captured {
		if cfg.Captured[i].Icon == "" {
			cfg.Captured[i].Icon = extractIconDataURL(cfg.Captured[i].Path)
		}
	}
	return cfg
}

func saveConfig(cfg Config) error {
	dir := filepath.Dir(getConfigPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// 图标不落盘，避免 json 体积爆炸。
	strip := func(list []AppInfo) []AppInfo {
		out := make([]AppInfo, 0, len(list))
		for _, a := range list {
			out = append(out, AppInfo{Name: a.Name, Path: a.Path})
		}
		return out
	}
	persist := Config{
		Manual:      strip(cfg.Manual),
		Captured:    strip(cfg.Captured),
		AutoStart:   cfg.AutoStart,
		AutoRestore: cfg.AutoRestore,
	}
	data, err := json.MarshalIndent(persist, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0644)
}

// GetConfig 供前端初始化时拉取全部数据。
// 关键：返回 JSON 字符串而不是 Config struct。
// 原因：Wails v2 的 method binding 对 struct 返回值走的是它自己的反射路径，
// 在某些 WebView2 + 复杂嵌套类型（slice of struct）的组合下，字段会被吞掉
// 表现为「后端有 11 个 app，前端拿到的 manual/captured 是空数组」。
// 改用 string 走原生 json.Marshal 输出，序列化路径完全可控，避免此坑。
func (a *App) GetConfig() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, err := json.Marshal(a.cfg)
	if err != nil {
		debugLog("GetConfig marshal err: %v", err)
		return `{"manual":[],"captured":[],"auto_start":false,"auto_restore":false}`
	}
	debugLog("GetConfig returns %d bytes: manual=%d captured=%d",
		len(data), len(a.cfg.Manual), len(a.cfg.Captured))
	return string(data)
}

// SaveConfig 手动保存。
func (a *App) SaveConfig() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return saveConfig(a.cfg)
}

// ============ 图标提取（HICON -> PNG data URL）============

// extractIconDataURL 从 exe 提取指定尺寸图标并编码为 PNG data URL。
func extractIconDataURL(path string) string {
	url, err := extractIcon(path, 64)
	if err != nil {
		return ""
	}
	return url
}

func extractIcon(path string, size int) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	var hicon win.HICON
	win.SHDefExtractIcon(
		syscall.StringToUTF16Ptr(path),
		0,
		0,
		nil,
		&hicon,
		win.MAKELONG(0, uint16(size)))
	if hicon == 0 {
		return "", fmt.Errorf("no icon for %s", path)
	}
	defer win.DestroyIcon(hicon)
	return iconToPNG(hicon, size)
}

func iconToPNG(hicon win.HICON, size int) (string, error) {
	hdc := win.GetDC(0)
	if hdc == 0 {
		return "", fmt.Errorf("GetDC failed")
	}
	defer win.ReleaseDC(0, hdc)

	memDC := win.CreateCompatibleDC(hdc)
	if memDC == 0 {
		return "", fmt.Errorf("CreateCompatibleDC failed")
	}
	defer win.DeleteDC(memDC)

	var bmi win.BITMAPINFO
	bmi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bmi.BmiHeader))
	bmi.BmiHeader.BiWidth = int32(size)
	bmi.BmiHeader.BiHeight = -int32(size) // 负数表示自上而下
	bmi.BmiHeader.BiPlanes = 1
	bmi.BmiHeader.BiBitCount = 32
	bmi.BmiHeader.BiCompression = win.BI_RGB

	var bits unsafe.Pointer
	hbm := win.CreateDIBSection(memDC, &bmi.BmiHeader, win.DIB_RGB_COLORS, &bits, 0, 0)
	if hbm == 0 {
		return "", fmt.Errorf("CreateDIBSection failed")
	}
	defer win.DeleteObject(win.HGDIOBJ(hbm))

	old := win.SelectObject(memDC, win.HGDIOBJ(hbm))
	defer win.SelectObject(memDC, old)

	if !win.DrawIconEx(memDC, 0, 0, hicon, int32(size), int32(size), 0, 0, win.DI_NORMAL) {
		return "", fmt.Errorf("DrawIconEx failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	stride := size * 4
	base := uintptr(bits)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			off := uintptr(y*stride + x*4)
			b := *(*uint8)(unsafe.Pointer(base + off))
			g := *(*uint8)(unsafe.Pointer(base + off + 1))
			r := *(*uint8)(unsafe.Pointer(base + off + 2))
			al := *(*uint8)(unsafe.Pointer(base + off + 3))
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: al})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// ============ 列表操作 ============

// AddApp 弹出文件选择框加入手动区。
func (a *App) AddApp() (bool, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择软件",
		Filters: []runtime.FileFilter{
			{DisplayName: "可执行文件 (*.exe)", Pattern: "*.exe"},
			{DisplayName: "快捷方式 (*.lnk)", Pattern: "*.lnk"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return false, err
	}
	if path == "" {
		return false, nil
	}
	if strings.EqualFold(filepath.Ext(path), ".lnk") {
		if target := shortcutTarget(path); target != "" {
			path = target
		} else {
			return false, fmt.Errorf("快捷方式解析失败")
		}
	}
	return a.addPath(path), nil
}

// AddPaths 处理窗口拖放的文件。
func (a *App) AddPaths(paths []string) int {
	added := 0
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".exe" && ext != ".lnk" {
			continue
		}
		if ext == ".lnk" {
			if t := shortcutTarget(p); t != "" {
				p = t
			} else {
				continue
			}
		}
		if a.addPath(p) {
			added++
		}
	}
	return added
}

func (a *App) addPath(path string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, item := range a.cfg.Manual {
		if strings.EqualFold(item.Path, path) {
			return false
		}
	}
	a.cfg.Manual = append(a.cfg.Manual, AppInfo{
		Name: filepath.Base(path),
		Path: path,
		Icon: extractIconDataURL(path),
	})
	return true
}

// RemoveApp 删除指定区域的第 idx 项。
func (a *App) RemoveApp(zone string, idx int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := a.listRef(zone)
	if list == nil || idx < 0 || idx >= len(*list) {
		return fmt.Errorf("索引越界")
	}
	*list = append((*list)[:idx], (*list)[idx+1:]...)
	return saveConfig(a.cfg)
}

func (a *App) listRef(zone string) *[]AppInfo {
	if zone == "manual" {
		return &a.cfg.Manual
	}
	return &a.cfg.Captured
}

// ============ 启动 ============

func (a *App) LaunchApp(zone string, idx int) error {
	a.mu.Lock()
	list := a.listRef(zone)
	if list == nil || idx < 0 || idx >= len(*list) {
		a.mu.Unlock()
		return fmt.Errorf("索引越界")
	}
	item := (*list)[idx]
	a.mu.Unlock()
	return launch(item)
}

func launch(item AppInfo) error {
	if _, err := os.Stat(item.Path); os.IsNotExist(err) {
		return fmt.Errorf("文件不存在: %s", item.Path)
	}
	return exec.Command(item.Path).Start()
}

// LaunchAll 按序错峰启动两个区域的全部软件，返回汇总结果。
func (a *App) LaunchAll() string {
	a.mu.Lock()
	all := append(append([]AppInfo{}, a.cfg.Manual...), a.cfg.Captured...)
	a.mu.Unlock()

	if len(all) == 0 {
		return "列表为空，没有可启动的软件"
	}
	go func() {
		var failed []string
		for i, item := range all {
			if err := launch(item); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", item.Name, err))
			}
			if i < len(all)-1 {
				time.Sleep(time.Second)
			}
		}
		success := len(all) - len(failed)
		msg := fmt.Sprintf("已启动 %d 个软件", success)
		if len(failed) > 0 {
			msg += fmt.Sprintf("，%d 个失败：\n%s", len(failed), strings.Join(failed, "\n"))
		}
		// 通过事件把结果推给前端，避免在 goroutine 里直接操作 UI。
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "launch-result", msg)
		}
	}()
	return fmt.Sprintf("正在启动 %d 个软件…", len(all))
}

// ============ 抓取运行中的软件 ============

func (a *App) CaptureRunning() (int, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command",
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+
			"Get-Process | Where-Object { $_.MainWindowHandle -ne 0 -and $_.Path } | Select-Object -ExpandProperty Path")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	self, _ := os.Executable()
	manualSet := make(map[string]bool)
	a.mu.Lock()
	for _, item := range a.cfg.Manual {
		manualSet[strings.ToLower(item.Path)] = true
	}
	a.mu.Unlock()

	var captured []AppInfo
	seen := make(map[string]bool)
	for _, raw := range strings.Split(string(out), "\n") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if strings.EqualFold(p, self) || isSystemPath(p) || isAuxiliary(p) {
			continue
		}
		key := strings.ToLower(p)
		if manualSet[key] || seen[key] {
			continue
		}
		seen[key] = true
		captured = append(captured, AppInfo{
			Name: filepath.Base(p),
			Path: p,
			Icon: extractIconDataURL(p),
		})
	}

	a.mu.Lock()
	a.cfg.Captured = captured
	_ = saveConfig(a.cfg)
	a.mu.Unlock()
	return len(captured), nil
}

func isSystemPath(p string) bool {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return strings.HasPrefix(strings.ToUpper(p), strings.ToUpper(filepath.Clean(root)))
}

var auxiliaryKeywords = []string{
	"crashpad", "helper", "sdk", "webview", "widget",
	"updater", "update_", "installer", "setup_", "service",
}

func isAuxiliary(exePath string) bool {
	name := strings.ToLower(filepath.Base(exePath))
	for _, kw := range auxiliaryKeywords {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

func shortcutTarget(lnk string) string {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command",
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+
			"$sh = New-Object -ComObject WScript.Shell; $sc = $sh.CreateShortcut('"+lnk+"'); $sc.TargetPath")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ============ 开机自启（HKCU Run）============

const (
	runKeyName = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "AppStarter"
)

func (a *App) SetAutoStart(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyName, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表失败: %v", err)
	}
	defer k.Close()
	if !enabled {
		_ = k.DeleteValue(runValue)
	} else {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := k.SetStringValue(runValue, `"`+exe+`"`); err != nil {
			return fmt.Errorf("写入启动项失败: %v", err)
		}
	}
	a.mu.Lock()
	a.cfg.AutoStart = enabled
	_ = saveConfig(a.cfg)
	a.mu.Unlock()
	return nil
}

func (a *App) SetAutoRestore(enabled bool) error {
	a.mu.Lock()
	a.cfg.AutoRestore = enabled
	_ = saveConfig(a.cfg)
	a.mu.Unlock()
	return nil
}

func autoStartRegistered() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyName, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func syncAutoStart(enabled bool) {
	if enabled == autoStartRegistered() {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyName, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if !enabled {
		_ = k.DeleteValue(runValue)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = k.SetStringValue(runValue, `"`+exe+`"`)
}
