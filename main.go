package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

// version 由构建脚本通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

// updateRepo 为 GitHub 仓库 "owner/repo"，通过 -ldflags "-X main.updateRepo=..." 注入；
// 留空则禁用更新检查。
var updateRepo = ""

const (
	defaultHost    = "127.0.0.1"
	defaultPort    = 3080
	startupTimeout = 180 * time.Second

	updateCheckInterval = 6 * time.Hour
	updateStartupDelay  = 15 * time.Second
)

var (
	childMu        sync.Mutex
	childCmd       *exec.Cmd
	logFile        *os.File
	jobHandle      windows.Handle
	mainWindow     application.Window
	currentPort    int
	currentLogPath string
)

func main() {
	flagPort := flag.Int("port", 0, "DeepSeek Harness web 端口（默认随机空闲端口）")
	flag.Parse()

	port := resolvePort(*flagPort)

	app := application.New(application.Options{
		Name:        "dsh-desktop",
		Description: "DeepSeek Harness desktop shell built with Go + Wails",
		OnShutdown:  cleanup,
	})

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "DeepSeek Harness",
		Width:            1280,
		Height:           820,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(16, 18, 22),
		HTML:             loadingHTML,
	})

	mainWindow = window
	window.Center()

	setMenu(app)
	startUpdateChecker(app)

	go bootstrap(window, port)

	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run error:", err)
		os.Exit(1)
	}
}

func resolvePort(flagPort int) int {
	if flagPort > 0 && flagPort < 65536 {
		return flagPort
	}
	if env := os.Getenv("DSH_PORT"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return 0 // 0 表示自动挑选一个空闲的 127.0.0.1 端口
}

// bootstrap 负责把 DeepSeek Harness 的本地 Web UI 带起来，并把窗口导航过去。
// port 为 0 时自动挑选一个空闲的 127.0.0.1 端口。
func bootstrap(w application.Window, port int) {
	if port == 0 {
		port = findFreePort()
	}
	currentPort = port
	url := fmt.Sprintf("http://%s:%d", defaultHost, port)

	// 1. 停止可能残留的自己启动的子进程（例如重启场景）。
	stopChild()

	// 2. 如果服务已在运行（例如用户手动启动过），直接复用。
	if probeReady(url) {
		w.SetURL(url)
		return
	}

	// 3. 启动子进程。
	cmd, logPath := startServer(port)
	if cmd == nil {
		w.SetHTML(errorHTML("无法启动 DeepSeek Harness。", logPath, url))
		return
	}

	childMu.Lock()
	childCmd = cmd
	childMu.Unlock()

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	// 4. 轮询直到服务就绪、子进程退出或超时。
	if waitReady(url, startupTimeout, exited) {
		w.SetURL(url)
		return
	}

	w.SetHTML(errorHTML("等待 DeepSeek Harness 启动超时。", logPath, url))
}

func startServer(port int) (*exec.Cmd, string) {
	logPath := filepath.Join(userDataDir(), "logs", "dsh-desktop.log")
	currentLogPath = logPath
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)

	var out io.Writer = io.Discard
	if f, err := os.Create(logPath); err == nil {
		logFile = f
		out = f
	}

	nodePath := findNode()
	binJS := findDshBin()

	var cmd *exec.Cmd
	if nodePath != "" && binJS != "" {
		// 离线模式：直接用内置 node 运行预置的 dsh 入口。
		cmd = newHiddenCmd(nodePath, binJS, "web", "--port", strconv.Itoa(port))
	} else {
		// 回退：运行时未预置 dsh 时，退回 npx 在线安装。
		commandLine := fmt.Sprintf(`npx -y @deepseek-ai/dsh web --port %d`, port)
		cmd = newHiddenCmd("cmd", "/c", commandLine)
	}
	// 应用专属启动目录：dsh 的 profiles/sessions/plugins 都存到这里，
	// 与安装目录分离，升级不会删除用户数据。
	cmd.Dir = launchRoot()
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		return nil, logPath
	}
	// 把子进程挂到 Job Object，KILL_ON_JOB_CLOSE 保证主进程退出时
	// 即使不走正常清理路径，整棵子进程树也会被系统强制回收。
	assignToJobObject(cmd.Process.Pid)
	return cmd, logPath
}

// findNode 返回 node 可执行文件路径（空串表示未找到）。
// 查找顺序：exe 同目录 runtime/node/node.exe → PATH。
func findNode() string {
	exe, err := os.Executable()
	if err == nil {
		bundled := filepath.Join(filepath.Dir(exe), "runtime", "node", "node.exe")
		if fileExists(bundled) {
			return bundled
		}
	}
	if p, err := exec.LookPath("node"); err == nil {
		return p
	}
	return ""
}

// findDshBin 返回预置的 dsh 入口脚本路径（空串表示未预置）。
// 查找顺序：exe 同目录 runtime/ → exe 同目录 .dsh-runtime/（开发模式）。
func findDshBin() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exeDir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(exeDir, "runtime", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"),
		filepath.Join(exeDir, ".dsh-runtime", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"),
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func newHiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func probeReady(url string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

func waitReady(url string, timeout time.Duration, exited <-chan struct{}) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1200 * time.Millisecond}

	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return false
		default:
		}

		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

// stopChild 停止由本应用启动的 Harness 子进程。
func stopChild() {
	childMu.Lock()
	cmd := childCmd
	childCmd = nil
	childMu.Unlock()

	if cmd != nil && cmd.Process != nil {
		killTree(cmd.Process.Pid)
	}
	if jobHandle != 0 {
		_ = windows.TerminateJobObject(jobHandle, 1)
		_ = windows.CloseHandle(jobHandle)
		jobHandle = 0
	}
}

func cleanup() {
	stopChild()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
}

// assignToJobObject 把进程 pid 挂到一个 KILL_ON_JOB_CLOSE 的 Job Object 上。
// Job 句柄保存在包级变量中，进程存活期间保持打开；一旦主进程退出，
// 句柄被系统关闭，Job 内的所有进程都会被终止。
func assignToJobObject(pid int) {
	if runtime.GOOS != "windows" {
		return
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return
	}

	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(proc)

	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return
	}

	jobHandle = job
}

func killTree(pid int) {
	if runtime.GOOS != "windows" {
		return
	}
	_ = newHiddenCmd("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

// userDataDir 返回应用数据根目录（%LOCALAPPDATA%\dsh-desktop）。
func userDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "dsh-desktop")
}

// launchRoot 返回 dsh 子进程的工作目录（应用专属启动目录）。
func launchRoot() string {
	dir := filepath.Join(userDataDir(), "launch-root")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// findFreePort 返回一个空闲的 127.0.0.1 端口。
func findFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return defaultPort
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// setMenu 构建应用菜单：重启 Harness、打开日志、退出。
func setMenu(app *application.App) {
	menu := app.Menu.New()

	harnessSub := menu.AddSubmenu("Harness")
	harnessSub.Add("Restart Harness").OnClick(func(_ *application.Context) {
		if mainWindow != nil {
			go bootstrap(mainWindow, currentPort)
		}
	})
	harnessSub.AddSeparator()
	harnessSub.Add("Open Log").OnClick(func(_ *application.Context) {
		openLog()
	})
	harnessSub.AddSeparator()
	harnessSub.Add("Check for Updates…").OnClick(func(_ *application.Context) {
		checkForUpdates(app, true)
	})

	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(_ *application.Context) {
		app.Quit()
	})

	app.Menu.SetApplicationMenu(menu)
}

// openLog 用记事本打开日志文件。
func openLog() {
	if currentLogPath == "" || !fileExists(currentLogPath) {
		currentLogPath = filepath.Join(userDataDir(), "logs", "dsh-desktop.log")
	}
	_ = newHiddenCmd("notepad.exe", currentLogPath).Start()
}

// latestRelease 是 GitHub Releases API 返回的关键字段。
type latestRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
}

// startUpdateChecker 启动后延迟检查一次，并每 6 小时检查一次。
func startUpdateChecker(app *application.App) {
	if updateRepo == "" {
		return
	}
	go func() {
		time.Sleep(updateStartupDelay + time.Duration(rand.Intn(15000))*time.Millisecond)
		checkForUpdates(app, false)
		ticker := time.NewTicker(updateCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			checkForUpdates(app, false)
		}
	}()
}

// checkForUpdates 从 GitHub Releases 检查新版本；manual 为 true 时总是给出提示。
func checkForUpdates(app *application.App, manual bool) {
	if updateRepo == "" {
		if manual {
			showInfoDialog(app, "未配置更新源", "构建时通过 -UpdateRepo 指定 GitHub 仓库（owner/repo）后即可检查更新。")
		}
		return
	}
	rel, err := fetchLatestRelease(updateRepo)
	if err != nil {
		if manual {
			showInfoDialog(app, "检查更新失败", err.Error())
		}
		return
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if compareVersions(latest, version) > 0 {
		showUpdateDialog(app, rel, latest)
	} else if manual {
		showInfoDialog(app, "已是最新版本", "当前版本 "+version)
	}
}

// fetchLatestRelease 查询 GitHub 上指定仓库的最新 Release。
func fetchLatestRelease(repo string) (*latestRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dsh-desktop")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	var rel latestRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release 信息缺失")
	}
	return &rel, nil
}

// compareVersions 比较两个形如 "1.2.3"（可带 v 前缀）的版本号：
// a > b 返回 1，a < b 返回 -1，相等返回 0。忽略预发布后缀。
func compareVersions(a, b string) int {
	a = strings.SplitN(strings.TrimPrefix(a, "v"), "-", 2)[0]
	b = strings.SplitN(strings.TrimPrefix(b, "v"), "-", 2)[0]
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// showUpdateDialog 弹出新版本提示，可打开下载页。
func showUpdateDialog(app *application.App, rel *latestRelease, latest string) {
	msg := fmt.Sprintf("发现新版本 %s（当前 %s）\n\n%s", latest, version, rel.HTMLURL)
	dlg := app.Dialog.Question().
		SetTitle("发现新版本").
		SetMessage(msg)
	if mainWindow != nil {
		dlg.AttachToWindow(mainWindow)
	}
	dlg.AddButton("打开下载页").OnClick(func() {
		openBrowser(rel.HTMLURL)
	})
	dlg.AddButton("稍后")
	dlg.Show()
}

// showInfoDialog 弹出信息提示框。
func showInfoDialog(app *application.App, title, msg string) {
	dlg := app.Dialog.Info().SetTitle(title).SetMessage(msg)
	if mainWindow != nil {
		dlg.AttachToWindow(mainWindow)
	}
	dlg.Show()
}

// openBrowser 用系统默认浏览器打开 url。
func openBrowser(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

const loadingHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>DeepSeek Harness</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    min-height: 100vh; display: flex; align-items: center; justify-content: center;
    background: radial-gradient(1200px 800px at 50% -10%, #1c2536 0%, #101216 55%, #0c0e11 100%);
    color: #e6e8eb; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft YaHei", sans-serif;
  }
  .card { text-align: center; padding: 48px; }
  .mark { display: inline-flex; align-items: center; gap: 12px; font-size: 24px; font-weight: 600; letter-spacing: .5px; }
  .mark .dot { width: 14px; height: 14px; border-radius: 50%; background: #4d6bfe; box-shadow: 0 0 18px #4d6bfe; }
  .spinner { margin: 36px auto 20px; width: 34px; height: 34px; border-radius: 50%;
    border: 3px solid rgba(255,255,255,.12); border-top-color: #4d6bfe; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .status { color: #9aa3ad; font-size: 14px; line-height: 1.8; }
  .hint { margin-top: 26px; color: #5b6470; font-size: 12px; }
</style>
</head>
<body>
  <div class="card">
    <div class="mark"><span class="dot"></span>DeepSeek Harness</div>
    <div class="spinner"></div>
    <div class="status">正在启动 DeepSeek Harness…</div>
    <div class="hint">首次运行会自动下载 <code>@deepseek-ai/dsh</code>，可能需要几分钟。</div>
  </div>
</body>
</html>`

func errorHTML(msg, logPath, url string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"/><meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>DeepSeek Harness</title>
<style>
  :root { color-scheme: dark; }
  body { min-height:100vh; margin:0; display:flex; align-items:center; justify-content:center;
    background:#0f1115; color:#e6e8eb; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif; }
  .card { max-width:640px; padding:40px; text-align:center; }
  .err { display:inline-block; width:44px; height:44px; line-height:44px; border-radius:50%%;
    background:#3a1d1d; color:#f26d6d; font-size:24px; margin-bottom:20px; }
  h1 { font-size:20px; font-weight:600; margin:0 0 12px; }
  p { color:#9aa3ad; font-size:14px; line-height:1.8; margin:6px 0; }
  code { background:#1a1d23; padding:2px 8px; border-radius:6px; color:#c9d1d9; font-size:13px; }
  .tip { margin-top:22px; color:#5b6470; font-size:12px; }
</style>
</head>
<body>
<div class="card">
  <div class="err">!</div>
  <h1>启动失败</h1>
  <p>%s</p>
  <p>目标地址：<code>%s</code></p>
  <p>日志文件：<code>%s</code></p>
  <p class="tip">请确认已安装 Node.js，然后关闭窗口重试。</p>
</div>
</body>
</html>`, html.EscapeString(msg), html.EscapeString(url), html.EscapeString(logPath))
}
