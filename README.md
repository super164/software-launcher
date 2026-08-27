<p align="center"><img src="assets/logo.svg" alt="AppStarter logo" width="128"></p>

# 软件启动器 (AppStarter)

> 一键保存 / 复活你正在用的所有软件。Windows 专用，单文件、零依赖、免安装。

每天开电脑、回来上班，都要把微信、浏览器、编辑器、音乐……一个个手动打开？
软件启动器帮你把「当前正在跑的软件」一键记下来，第二天（或重启后）一键全部拉起。

## ✨ 核心功能

- **手动添加**：文件对话框选 `.exe`，或把 `.exe` / `.lnk` 快捷方式直接拖进窗口。
- **自动抓取**：点一下「抓取运行软件」，自动记录当前所有**有可见窗口**的进程（已排除系统进程与常见辅助进程，并与手动区去重）。
- **一键启动**：按列表顺序**错峰**（间隔 1 秒）启动全部软件，避免瞬间卡顿；结束后汇总成功 / 失败明细。
- **持久保存**：列表存到 `%APPDATA%/Local/AppStarter/apps.json`，关掉程序也不丢。
- **单独启动**：双击任意列表项，单独拉起该软件。

## 🖼 界面预览

![界面预览](screenshots/preview.svg)

> 上为界面示意（SVG 还原布局）。你可以用 `Win + Shift + S` 截取真实运行图后替换 `screenshots/preview.svg`。

## 📦 安装

### 方式一：下载预编译（推荐，零门槛）
到 [Releases](../../releases) 页面下载 `AppStarter.exe`，**双击即可运行**，无需安装 Go，无需任何运行时。

### 方式二：从源码构建
环境要求：Windows + Go 1.24+

```bash
# 1. 安装 rsrc（把 manifest 嵌进 exe，否则 walk 会闪退）
go install github.com/akavel/rsrc@latest

# 2. 生成 rsrc.syso（也可直接运行 build.bat 自动完成）
rsrc -manifest AppStarter.exe.manifest -o rsrc.syso

# 3. 以 GUI 子系统编译（无黑框控制台）
go build -ldflags="-H windowsgui" -o AppStarter.exe .
```

或直接执行仓库里的 `build.bat`（已封装上述步骤）。

## 🚀 使用

1. **添加常用软件**：点「添加软件」选 `.exe`，或直接把程序 / 快捷方式拖进窗口。
2. **下班 / 重启前**：点「抓取运行软件」，把当前开着的所有软件快照保存下来。
3. **第二天 / 开机后**：点「启动所有软件」，按顺序一键全部复活。
4. **手动区**用于「固定常用」，**自动抓取区**用于「临时快照」，两者自动去重，不会重复启动。

## 🛠 技术栈

- **Go 1.24**
- [github.com/lxn/walk](https://github.com/lxn/walk) 声明式 Win32 GUI（纯 Go，无 CGO，无外部运行时）
- 配置持久化：`encoding/json` → `%APPDATA%/Local/AppStarter/apps.json`
- 抓取逻辑：PowerShell `Get-Process` 取有窗口进程 + 过滤规则

## ⚠️ 局限 / 已知问题

- **仅支持 Windows**（walk 为 Windows-only）。
- **「自动抓取」是启发式**：基于「有可见主窗口的进程」判断，可能漏掉无窗口的后台工具，或依赖内置过滤关键词；已排除系统目录与 `crashpad/helper/updater/service` 等辅助进程，但仍非 100% 精准。
- **walk 库版本较旧**（2021），未来 Windows 大版本更新时请留意兼容性。

## 🤝 贡献

欢迎提 Issue / PR。本地开发只需 Windows + Go 1.24+，按上方「从源码构建」即可跑起来。

## 📄 许可证

[MIT](LICENSE) —— 随便用、随便改，留个版权声明即可。
