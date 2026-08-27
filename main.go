package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// AppInfo 单条软件记录：名称 + 完整路径。
type AppInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Config 落盘结构：拆分为「手动区」与「自动抓取区」两块，互不污染。
type Config struct {
	Manual   []AppInfo `json:"manual"`
	Captured []AppInfo `json:"captured"`
}

var (
	manualApps   []AppInfo // 手动添加区：添加软件/拖拽加入，持久保存，不受抓取影响
	capturedApps []AppInfo // 自动抓取区：每次「抓取运行软件」重建为当前运行快照，与手动区去重
	mainWindow   *walk.MainWindow
	manualList   *walk.ListBox // 手动区列表控件
	capturedList *walk.ListBox // 自动区列表控件
)

// getConfigPath 返回配置文件位置：%APPDATA%/Local/AppStarter/apps.json
func getConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "AppData", "Local", "AppStarter", "apps.json")
}

// displayManual / displayCaptured 生成各自列表的显示串（"名称 - 路径"）。
func displayManual() []string {
	items := make([]string, 0, len(manualApps))
	for _, a := range manualApps {
		items = append(items, a.Name+"  -  "+a.Path)
	}
	return items
}

func displayCaptured() []string {
	items := make([]string, 0, len(capturedApps))
	for _, a := range capturedApps {
		items = append(items, a.Name+"  -  "+a.Path)
	}
	return items
}

func refreshManual() {
	if manualList != nil {
		_ = manualList.SetModel(displayManual())
	}
}

func refreshCaptured() {
	if capturedList != nil {
		_ = capturedList.SetModel(displayCaptured())
	}
}

// loadConfig 启动时读取已有配置；出错时明确提示而不是静默忽略。
func loadConfig() {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return // 首次运行，没有配置文件，属正常情况
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		walk.MsgBox(nil, "配置错误", "读取配置失败: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	manualApps = cfg.Manual
	capturedApps = cfg.Captured
}

// saveConfig 把两块列表写入 apps.json（手动区与自动区都持久化，
// 这样第二天开机也能凭自动区内容一键复活）。
func saveConfig() {
	dir := filepath.Dir(getConfigPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		walk.MsgBox(mainWindow, "错误", "创建目录失败: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	cfg := Config{Manual: manualApps, Captured: capturedApps}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		walk.MsgBox(mainWindow, "错误", "序列化失败: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	if err := os.WriteFile(getConfigPath(), data, 0644); err != nil {
		walk.MsgBox(mainWindow, "错误", "写入文件失败: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	walk.MsgBox(mainWindow, "成功", "已保存！\n位置: "+getConfigPath(), walk.MsgBoxIconInformation)
}

// addPath 把一条路径加入手动区（去重）。返回是否真正新增。
func addPath(path string) bool {
	for _, a := range manualApps {
		if a.Path == path {
			walk.MsgBox(mainWindow, "提示", "该软件已在手动列表中", walk.MsgBoxIconInformation)
			return false
		}
	}
	manualApps = append(manualApps, AppInfo{Name: filepath.Base(path), Path: path})
	return true
}

// addApp 通过文件对话框添加 .exe 到手动区。
func addApp() {
	dlg := new(walk.FileDialog)
	dlg.Title = "选择软件"
	dlg.Filter = "可执行文件 (*.exe)|*.exe|所有文件 (*.*)|*.*"
	accepted, err := dlg.ShowOpen(mainWindow)
	if err != nil {
		walk.MsgBox(mainWindow, "错误", err.Error(), walk.MsgBoxIconError)
		return
	}
	if !accepted || dlg.FilePath == "" {
		return
	}
	if addPath(dlg.FilePath) {
		refreshManual()
		saveConfig()
	}
}

// deleteSelected 删除当前聚焦列表的选中项（先在手动区找，再在自动区找）。
func deleteSelected() {
	if idx := manualList.CurrentIndex(); idx >= 0 && idx < len(manualApps) {
		manualApps = append(manualApps[:idx], manualApps[idx+1:]...)
		refreshManual()
		saveConfig()
		return
	}
	if idx := capturedList.CurrentIndex(); idx >= 0 && idx < len(capturedApps) {
		capturedApps = append(capturedApps[:idx], capturedApps[idx+1:]...)
		refreshCaptured()
		saveConfig()
		return
	}
	walk.MsgBox(mainWindow, "提示", "请先选择要删除的软件", walk.MsgBoxIconInformation)
}

// launchApp 启动单个软件，返回错误（文件不存在 / 启动失败）以便上层汇总。
func launchApp(a AppInfo) error {
	if _, err := os.Stat(a.Path); os.IsNotExist(err) {
		return fmt.Errorf("文件不存在: %s", a.Path)
	}
	cmd := exec.Command(a.Path)
	// 不等待、不阻塞；启动器本身可以继续响应。
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

// launchAll 按列表顺序依次启动「手动区 + 自动区」全部软件（错峰）：每个之间间隔 1 秒，
// 避免瞬间同时拉起造成卡顿。启动过程放后台 goroutine，不阻塞 UI 消息循环；
// 结束用 Synchronize 切回主线程弹结果。两块已去重，不会重复启动同一软件。
func launchAll() {
	all := append(append([]AppInfo{}, manualApps...), capturedApps...)
	if len(all) == 0 {
		walk.MsgBox(mainWindow, "提示", "两个列表都为空，请先添加或抓取软件", walk.MsgBoxIconInformation)
		return
	}
	go func() {
		var failed []string
		for i, a := range all {
			if err := launchApp(a); err != nil {
				failed = append(failed, fmt.Sprintf("• %s: %v", a.Name, err))
			}
			if i < len(all)-1 {
				time.Sleep(time.Second)
			}
		}
		success := len(all) - len(failed)
		mainWindow.Synchronize(func() {
			if len(failed) == 0 {
				walk.MsgBox(mainWindow, "启动结果", fmt.Sprintf("已按序启动完成！\n成功: %d", success), walk.MsgBoxIconInformation)
			} else {
				walk.MsgBox(mainWindow, "启动结果",
					fmt.Sprintf("成功: %d\n失败: %d\n\n失败明细:\n%s", success, len(failed), strings.Join(failed, "\n")),
					walk.MsgBoxIconWarning)
			}
		})
	}()
}

// launchSelected 启动当前聚焦列表的选中项（单选）。
func launchSelected() {
	if idx := manualList.CurrentIndex(); idx >= 0 && idx < len(manualApps) {
		a := manualApps[idx]
		if err := launchApp(a); err != nil {
			walk.MsgBox(mainWindow, "启动失败", fmt.Sprintf("启动 %s 失败:\n%v", a.Name, err), walk.MsgBoxIconError)
			return
		}
		walk.MsgBox(mainWindow, "启动成功", fmt.Sprintf("已启动: %s", a.Name), walk.MsgBoxIconInformation)
		return
	}
	if idx := capturedList.CurrentIndex(); idx >= 0 && idx < len(capturedApps) {
		a := capturedApps[idx]
		if err := launchApp(a); err != nil {
			walk.MsgBox(mainWindow, "启动失败", fmt.Sprintf("启动 %s 失败:\n%v", a.Name, err), walk.MsgBoxIconError)
			return
		}
		walk.MsgBox(mainWindow, "启动成功", fmt.Sprintf("已启动: %s", a.Name), walk.MsgBoxIconInformation)
		return
	}
	walk.MsgBox(mainWindow, "提示", "请先选择要启动的软件", walk.MsgBoxIconInformation)
}

// isSystemPath 判断 exe 是否位于 Windows 系统目录，用于抓取时过滤掉系统/外壳进程。
func isSystemPath(p string) bool {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return strings.HasPrefix(strings.ToUpper(p), strings.ToUpper(filepath.Clean(root)))
}

// auxiliaryKeywords 是辅助进程/子进程的文件名关键词黑名单（轻量兜底）。
// 抓取只取有可见窗口的进程后，仍用此排除明显不是主程序的东西。
var auxiliaryKeywords = []string{
	"crashpad", "helper", "sdk", "webview", "widget",
	"updater", "update_", "installer", "setup_", "service",
}

// isAuxiliaryProcess 根据文件名判断是否为辅助/子进程（非主程序）。
func isAuxiliaryProcess(exePath string) bool {
	name := strings.ToLower(filepath.Base(exePath))
	for _, kw := range auxiliaryKeywords {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

// captureRunning 扫描当前「有可见主窗口」的进程，重建自动抓取区。
// 这是贴合用户初衷的核心功能：下班前点一下，把正在用的软件都记下来，第二天一键复活。
//
// 行为（重建式快照，天然实现"自动更新"）：
//   - 只抓有窗口的进程（最干净，几乎全是主程序）
//   - 排除自身、系统目录进程、辅助进程
//   - 与手动区去重：已在手动区的路径不进自动区（避免重复显示/重复启动）
//   - 每次重建 capturedApps：上次抓到、这次已关闭的自动消失；新开的自动加入
//
// 抓取放后台 goroutine，避免 PowerShell 冷启动期间阻塞 UI；
// 最终对 capturedApps 的修改统一回到主线程（Synchronize）执行。
func captureRunning() {
	go func() {
		// 强制 UTF-8 输出，避免中文 Windows 默认 GBK 导致中文路径变乱码（◆/□）。
		// 只取「有可见主窗口」的进程——回到最初最可靠的方式。
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command",
			"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+
				"Get-Process | Where-Object { $_.MainWindowHandle -ne 0 -and $_.Path } | Select-Object -ExpandProperty Path")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.Output()
		if err != nil {
			mainWindow.Synchronize(func() {
				walk.MsgBox(mainWindow, "错误", "抓取失败: "+err.Error(), walk.MsgBoxIconError)
			})
			return
		}

		self, _ := os.Executable()
		manualSet := make(map[string]bool, len(manualApps))
		for _, a := range manualApps {
			manualSet[a.Path] = true
		}

		// 重建自动区：遍历当前有窗口进程，过滤后形成新的 capturedApps（关掉的自然不在了）。
		var newCaptured []AppInfo
		for _, raw := range strings.Split(string(out), "\n") {
			p := strings.TrimSpace(raw)
			if p == "" {
				continue
			}
			if strings.EqualFold(p, self) || isSystemPath(p) || isAuxiliaryProcess(p) {
				continue
			}
			// 已在手动区 → 不进自动区（去重显示，避免重复）
			if manualSet[p] {
				continue
			}
			newCaptured = append(newCaptured, AppInfo{Name: filepath.Base(p), Path: p})
		}

		mainWindow.Synchronize(func() {
			capturedApps = newCaptured
			refreshCaptured()
			saveConfig() // 自动保存，第二天开机也能凭此一键复活
			walk.MsgBox(mainWindow, "抓取完成",
				fmt.Sprintf("已刷新自动抓取区：当前运行 %d 个软件（已在手动区的未重复计入）。\n保存列表后，明天开机点「启动所有软件」即可全部复活。", len(newCaptured)),
				walk.MsgBoxIconInformation)
		})
	}()
}

// getShortcutTarget 用 PowerShell 解析 .lnk 快捷方式指向的真实路径。
// 注意 TrimSpace：PowerShell 输出尾巴带 \r\n，不处理会导致启动失败（原版 bug）。
func getShortcutTarget(lnkPath string) string {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command",
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+
			"$sh = New-Object -ComObject WScript.Shell; $sc = $sh.CreateShortcut('"+lnkPath+"'); $sc.TargetPath")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// handleDropFiles 处理拖入窗口的文件：支持 .exe 与 .lnk，均加入「手动区」。
func handleDropFiles(files []string) {
	added := 0
	var skipped []string
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext != ".exe" && ext != ".lnk" {
			continue
		}
		path := f
		if ext == ".lnk" {
			target := getShortcutTarget(f)
			if target == "" {
				skipped = append(skipped, filepath.Base(f)+" (快捷方式解析失败)")
				continue
			}
			path = target
		}
		if addPath(path) {
			added++
		}
	}
	refreshManual()
	saveConfig()
	if added > 0 || len(skipped) > 0 {
		msg := fmt.Sprintf("已添加到手动区: %d", added)
		if len(skipped) > 0 {
			msg += "\n\n未添加:\n" + strings.Join(skipped, "\n")
		}
		walk.MsgBox(mainWindow, "拖拽结果", msg, walk.MsgBoxIconInformation)
	}
}

func main() {
	loadConfig()

	// 声明式 UI：walk 负责消息循环、布局与 DPI，原版"忙等占满 CPU"和"按钮被边框裁切"两个问题结构性消失。
	// 双 ListBox 布局：上=手动区，下=自动抓取区，各自带标题标签。
	_, err := (MainWindow{
		AssignTo: &mainWindow,
		Title:    "软件启动器",
		Size:     Size{Width: 600, Height: 560},
		MinSize:  Size{Width: 600, Height: 560},
		Layout:   VBox{},
		// walk 会自动启用窗口接受拖拽并回调此函数，传入干净的文件路径切片。
		OnDropFiles: handleDropFiles,
		Children: []Widget{
			Composite{
				Layout: HBox{
					Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 5},
					Spacing: 10,
				},
				Children: []Widget{
					PushButton{Text: "添加软件", OnClicked: addApp},
					PushButton{Text: "删除选中", OnClicked: deleteSelected},
					PushButton{Text: "保存列表", OnClicked: saveConfig},
					HSpacer{},
				},
			},
			Composite{
				Layout: HBox{
					Margins: Margins{Left: 10, Top: 0, Right: 10, Bottom: 10},
					Spacing: 10,
				},
				Children: []Widget{
					PushButton{Text: "抓取运行软件", OnClicked: captureRunning},
					PushButton{Text: "启动选中", OnClicked: launchSelected},
					PushButton{Text: "启动所有软件", OnClicked: launchAll},
					HSpacer{},
				},
			},
			Label{Text: "手动添加（持久保存，不会被抓取覆盖）"},
			ListBox{
				AssignTo: &manualList,
				MinSize:  Size{Height: 160},
				Model:    displayManual(),
				// 双击列表项 = 单独启动该项。
				OnItemActivated: func() {
					idx := manualList.CurrentIndex()
					if idx >= 0 && idx < len(manualApps) {
						if err := launchApp(manualApps[idx]); err != nil {
							walk.MsgBox(mainWindow, "启动失败", err.Error(), walk.MsgBoxIconError)
						}
					}
				},
			},
			Label{Text: "自动抓取（当前运行，每次抓取自动刷新；与手动区去重）"},
			ListBox{
				AssignTo: &capturedList,
				MinSize:  Size{Height: 160},
				Model:    displayCaptured(),
				OnItemActivated: func() {
					idx := capturedList.CurrentIndex()
					if idx >= 0 && idx < len(capturedApps) {
						if err := launchApp(capturedApps[idx]); err != nil {
							walk.MsgBox(mainWindow, "启动失败", err.Error(), walk.MsgBoxIconError)
						}
					}
				},
			},
		},
	}).Run()

	if err != nil {
		fmt.Fprintln(os.Stderr, "运行出错:", err)
		os.Exit(1)
	}
}
