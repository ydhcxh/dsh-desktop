# dsh-desktop

一个基于 **Go + Wails v3** 构建的 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) 本地优先桌面外壳。

[English](README.md)

## 为什么存在这个项目

DeepSeek Harness 已经提供了完整的 agent 运行时与 Web UI。dsh-desktop 不重新实现 Harness，而是补齐一个桌面产品所需的宿主能力：

- 无需手动启动 CLI 或管理本地端口
- 内置 Node.js 运行时与预装的 dsh 依赖——完全离线，目标机器无需安装 Node
- 应用专属启动目录，用户数据与安装目录分离
- 统一管理 Harness 子进程：启动、就绪检查、日志与关闭
- 可选的 GitHub Releases 版本检测

## 特性

- 启动后直接进入 Harness，无多余落地页
- 每次启动监听一个**随机的 `127.0.0.1` 端口**（或通过 `--port` 固定）
- 使用**内置的 `node.exe`** 运行**预装在 `runtime/` 里的 dsh 入口**——不依赖网络、不再走 `npx` 下载
- 复用**应用专属启动目录**（`%LOCALAPPDATA%\dsh-desktop\launch-root`）
- 菜单操作：重启子进程、查看日志、检查更新、退出
- 退出时优雅终止 Harness 子进程，并以 Windows **Job Object** 兜底（即使强杀主进程也能回收整棵子进程树）
- GitHub Releases 版本检测：启动后 + 每 6 小时 + 手动

## 环境要求

- **Node.js + npm** —— 仅构建时需要（用于安装 dsh 依赖与获取 Node 运行时）
- **Go 1.21+** —— 仅构建时需要
- **WebView2 Runtime** —— Windows 10/11 一般已内置（Wails v3 使用纯 Go 加载器，**无需 gcc/CGO**）

## 构建

```powershell
cd dsh-desktop
.\build.ps1                             # 安装依赖 + 编译 + 打包 node 与 dsh runtime
.\build.ps1 -Version 1.2.3              # 指定版本号
.\build.ps1 -DshVersion 0.1.0-rc.6      # 指定 dsh 版本
.\build.ps1 -NodeVersion 24.16.0        # 指定内置 Node 版本
.\build.ps1 -UpdateRepo "owner/repo"    # 启用 GitHub Releases 版本检测
.\build.ps1 -SkipDsh                    # 跳过 npm 安装（.dsh-runtime 已存在时）
```

构建产物：

```
bin/
├── dsh-desktop.exe      # 主程序（约 12 MB）
└── runtime/
    ├── node/            # 内置 Node 运行时（node.exe，约 92 MB）
    └── node_modules/    # 预装的 dsh 依赖（约 250 MB）
```

## 运行与分发

- 将 `dsh-desktop.exe` 与 `runtime/` 放在**同一目录**，双击 `dsh-desktop.exe` 即可，**目标机器无需安装 Node.js**。
- 首次进入后，在 `Settings → Models` 配置 DeepSeek API Key，并用 `Choose workspace` 选择项目目录。
- 分发时请把 `dsh-desktop.exe` 与 `runtime/` 一起打包（zip 或安装器，共约 350 MB）。

## 运行时架构

```
dsh-desktop (Go + Wails v3)
├── WebView2 窗口 ── DeepSeek Harness Web UI
│     └── http://127.0.0.1:<随机端口>
├── Harness 子进程生命周期（启动 / 就绪检查 / 重启 / 关闭）
├── Windows Job Object —— 即使强杀主进程也能回收整棵子进程树
├── 应用专属启动目录
└── 内置运行时
      ├── runtime/node/node.exe
      └── runtime/node_modules/@deepseek-ai/dsh/...

%LOCALAPPDATA%\dsh-desktop\
├── launch-root/            # dsh 工作目录（profiles / sessions / plugins）
└── logs/dsh-desktop.log    # 运行日志
```

## 端口配置

默认使用**随机空闲端口**。如需固定（优先级从高到低）：

1. 命令行参数：`dsh-desktop.exe --port 3090`
2. 环境变量：`$env:DSH_PORT="3090"`

## 版本检测

- 更新源：**GitHub Releases**（`https://api.github.com/repos/{owner}/{repo}/releases/latest`）
- 构建时通过 `-UpdateRepo "owner/repo"` 注入仓库地址；**留空则禁用**
- 检查节奏：启动后 15~30 秒首次检查，之后每 6 小时一次；菜单 `Harness → Check for Updates…` 可手动检查
- 发现新版会弹窗提示，并可一键打开下载页

## 数据与日志

- 用户数据（profiles / sessions / plugins）：`%LOCALAPPDATA%\dsh-desktop\launch-root\`
- 运行日志：`%LOCALAPPDATA%\dsh-desktop\logs\dsh-desktop.log`
- 菜单 `Harness → Restart Harness`：重启 dsh 服务
- 菜单 `Harness → Open Log`：用记事本打开运行日志

## 排错

- 启动失败 / 超时：查看 `%LOCALAPPDATA%\dsh-desktop\logs\dsh-desktop.log`（或菜单 `Harness → Open Log`）
- 固定端口冲突：改用其他 `--port`
- 更新 dsh：改 `build.ps1` 里的 `-DshVersion`，删除 `.dsh-runtime` 后重新构建
- 更新内置 Node：改 `build.ps1` 里的 `-NodeVersion`，删除 `bin\runtime\node` 后重新构建

## 上游版本

当前固定使用 `@deepseek-ai/dsh@0.1.0-rc.6`。DeepSeek Harness 处于开发者预览阶段、快速迭代，可能出现不兼容变更。

## 许可证

本项目采用 MIT 许可证。DeepSeek Harness 及其依赖遵循各自的上游许可证。
