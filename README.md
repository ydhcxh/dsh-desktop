# dsh-desktop

A local-first desktop shell for [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness), built with **Go + Wails v3**.

[简体中文文档](README.zh.md)

<img width="1264" height="781" alt="dsh-desktop-cove" src="https://github.com/user-attachments/assets/c5d608f5-3f20-42d9-9271-a0ed572660e3" />

## Why this project exists

DeepSeek Harness already provides a complete agent runtime and Web UI. dsh-desktop does not reimplement Harness; it supplies the host capabilities needed for a desktop product:

- Run without manually starting a CLI or managing local ports
- Bundled Node.js runtime and pre-installed dsh dependencies — fully offline, no Node install required on the target machine
- Application-owned launch directory keeps user data separate from the installation
- Manages the Harness child process, readiness checks, logs, and shutdown in one place
- Optional GitHub Releases version check

## Features

- Opens directly into Harness without a landing page
- Listens on a **random `127.0.0.1` port** for each launch (or a fixed `--port`)
- Starts Harness with the **bundled `node.exe`** and pre-installed `runtime/` — no network, no `npx` download
- Reuses the **application-owned launch directory** (`%LOCALAPPDATA%\dsh-desktop\launch-root`)
- Menu actions: restart the child process, view the log, check for updates, quit
- Gracefully terminates the Harness child process on exit, with a Windows **Job Object** as a safety net (the whole child tree is reclaimed even on a force-kill)
- GitHub Releases version check: on startup + every 6 hours + manual

## Requirements

- **Node.js + npm** — build time only (to install dsh deps and fetch the Node runtime)
- **Go 1.21+** — build time only
- **WebView2 Runtime** — preinstalled on Windows 10/11 (Wails v3 uses a pure-Go loader; **no gcc/CGO required**)

## Development

For local development, run the shell straight from the source tree — no full packaging required.

```powershell
# 0. Clone the repository
git clone https://github.com/ydhcxh/dsh-desktop.git
cd dsh-desktop

# 1. Install dsh deps into the local dev runtime (once)
npm install --prefix .dsh-runtime @deepseek-ai/dsh@0.1.0-rc.6 --no-audit --no-fund

# 2. Build & run from the source tree (or use `go run .`)
go build -o dsh-desktop.exe .
.\dsh-desktop.exe
```

How the runtime is resolved when launched this way:

1. **Node** — `runtime/node/node.exe` next to the exe, then `node` on `PATH`.
2. **dsh entry** — `runtime/node_modules/@deepseek-ai/dsh/lib/bin.js` next to the exe, then `.dsh-runtime/node_modules/@deepseek-ai/dsh/lib/bin.js`.
3. **Fallback** — if neither dsh entry exists, it runs `npx -y @deepseek-ai/dsh web ...` (online install).

With `.dsh-runtime/` prepared, step 2 runs fully offline using your system Node. For a distributable bundle (bundled Node + pre-installed deps), see [Build](#build).

## Build

```powershell
cd dsh-desktop
.\build.ps1                             # install deps + compile + bundle node & dsh runtime
.\build.ps1 -Version 1.2.3              # set the version
.\build.ps1 -DshVersion 0.1.0-rc.6      # pin the dsh version
.\build.ps1 -NodeVersion 24.16.0        # pin the bundled Node version
.\build.ps1 -UpdateRepo "owner/repo"    # enable the GitHub Releases update check
.\build.ps1 -SkipDsh                    # skip npm install (.dsh-runtime already present)
```

Artifacts:

```
bin/
├── dsh-desktop.exe      # main binary (~12 MB)
└── runtime/
    ├── node/            # bundled Node runtime (node.exe, ~92 MB)
    └── node_modules/    # pre-installed dsh dependencies (~250 MB)
```

### macOS

> **Note:** `main.go` is currently Windows-specific (`syscall.SysProcAttr{HideWindow}`, the Windows Job Object, `node.exe`, `taskkill`/`notepad`), so a plain cross-compile will not work yet — make the platform-specific code portable first. The commands below show the intended macOS packaging layout.

```bash
cd dsh-desktop

# 1. Install dsh deps (once)
npm install --prefix .dsh-runtime @deepseek-ai/dsh@0.1.0-rc.6 --no-audit --no-fund

# 2. Build for macOS (Apple Silicon; use GOARCH=amd64 for Intel)
GOOS=darwin GOARCH=arm64 go build -trimpath \
  -ldflags "-s -w -X main.version=1.2.3" \
  -o bin/dsh-desktop .

# 3. Bundle dsh deps
mkdir -p bin/runtime
cp -R .dsh-runtime/node_modules bin/runtime/node_modules

# 4. Bundle the Node runtime (darwin build)
mkdir -p bin/runtime/node
curl -fsSL "https://npmmirror.com/mirrors/node/v24.16.0/node-v24.16.0-darwin-arm64.tar.gz" -o /tmp/node.tar.gz
tar -xzf /tmp/node.tar.gz --strip-components=1 -C bin/runtime/node
rm /tmp/node.tar.gz
```

Ship `bin/dsh-desktop` together with `bin/runtime/` (a `.app` bundle or a zip).

## Run & distribute

- Keep `dsh-desktop.exe` and `runtime/` in the **same directory**, then double-click `dsh-desktop.exe`. The target machine needs **no Node.js**.
- On first launch, configure a DeepSeek API key in `Settings → Models` and pick a project directory with `Choose workspace`.
- Ship `dsh-desktop.exe` together with `runtime/` (about 350 MB total; a zip or an installer).

## Runtime architecture

```
dsh-desktop (Go + Wails v3)
├── WebView2 window ── DeepSeek Harness Web UI
│     └── http://127.0.0.1:<random-port>
├── Harness child-process lifecycle (start / readiness / restart / shutdown)
├── Windows Job Object — reclaims the whole child tree even on force-kill
├── Application-owned launch directory
└── Bundled runtime
      ├── runtime/node/node.exe
      └── runtime/node_modules/@deepseek-ai/dsh/...

%LOCALAPPDATA%\dsh-desktop\
├── launch-root/            # dsh working directory (profiles / sessions / plugins)
└── logs/dsh-desktop.log    # runtime log
```

## Port configuration

Default: a **random free port**. To pin it (highest priority first):

1. CLI: `dsh-desktop.exe --port 3090`
2. Env: `$env:DSH_PORT="3090"`

## Version check

- Source: **GitHub Releases** (`https://api.github.com/repos/{owner}/{repo}/releases/latest`)
- Inject the repo at build time via `-UpdateRepo "owner/repo"`; leave it empty to disable
- Cadence: once 15–30 s after startup, then every 6 hours, plus manual `Harness → Check for Updates…`
- On a new version, a dialog offers a one-click "Open download page"

## Data & logs

- User data (profiles / sessions / plugins): `%LOCALAPPDATA%\dsh-desktop\launch-root\`
- Runtime log: `%LOCALAPPDATA%\dsh-desktop\logs\dsh-desktop.log`
- Menu `Harness → Restart Harness`: restart the dsh service
- Menu `Harness → Open Log`: open the log in Notepad

## Troubleshooting

- Startup failed / timed out: check `%LOCALAPPDATA%\dsh-desktop\logs\dsh-desktop.log` (or menu `Harness → Open Log`)
- Port conflict with a fixed port: use a different `--port`
- Update dsh: change `-DshVersion` in `build.ps1`, delete `.dsh-runtime`, then rebuild
- Update the bundled Node: change `-NodeVersion` in `build.ps1`, delete `bin\runtime\node`, then rebuild

## Upstream version

Pins `@deepseek-ai/dsh@0.1.0-rc.6`. DeepSeek Harness is in developer preview and evolves rapidly; expect compatibility-breaking changes.

## License

MIT (this project). DeepSeek Harness and its dependencies remain under their respective upstream licenses.

