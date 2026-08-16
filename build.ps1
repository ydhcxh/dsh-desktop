param(
    [string]$Version = "1.0.0",
    [string]$DshVersion = "0.1.0-rc.6",
    [string]$NodeVersion = "24.16.0",
    [string]$UpdateRepo = "",
    [switch]$SkipDsh
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

# 1. Prepare dsh runtime deps (offline, no network at run time)
if (-not $SkipDsh) {
    $dshDir = Join-Path $root ".dsh-runtime"
    New-Item -ItemType Directory -Force -Path $dshDir | Out-Null
    Write-Host "Installing @deepseek-ai/dsh@$DshVersion ..."
    npm install --prefix $dshDir "@deepseek-ai/dsh@$DshVersion" --no-audit --no-fund
    if ($LASTEXITCODE -ne 0) {
        Write-Error "npm install failed"
        exit $LASTEXITCODE
    }
}

# 2. Locate go
$go = $null
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if ($goCmd) {
    $go = $goCmd.Source
} elseif (Test-Path "D:\go\bin\go.exe") {
    $go = "D:\go\bin\go.exe"
} else {
    throw "go not found, please install Go and add it to PATH"
}

$outDir = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$out = Join-Path $outDir "dsh-desktop.exe"

Write-Host "Using Go: $go"
Write-Host "Version: $Version"
if ($UpdateRepo) {
    Write-Host "Update repo: $UpdateRepo"
}

# -H=windowsgui produces a windowless GUI binary; -s -w strip symbols
$ldflags = "-H=windowsgui -s -w -X main.version=$Version"
if ($UpdateRepo) {
    $ldflags += " -X main.updateRepo=$UpdateRepo"
}
& $go build -trimpath -ldflags $ldflags -o $out .

if ($LASTEXITCODE -ne 0) {
    Write-Error "build failed"
    exit $LASTEXITCODE
}

# 3. Mirror runtime deps into bin\runtime (used by exe for offline startup)
$srcNM = Join-Path $root ".dsh-runtime\node_modules"
if (Test-Path $srcNM) {
    $dstNM = Join-Path $outDir "runtime\node_modules"
    Write-Host "Mirroring runtime deps into bin\runtime ..."
    New-Item -ItemType Directory -Force -Path $dstNM | Out-Null
    robocopy $srcNM $dstNM /MIR /NFL /NDL /NJH /NJS /NP | Out-Null
    # robocopy exit codes 0-7 are success, 8+ are errors
    if ($LASTEXITCODE -gt 7) {
        Write-Error "mirror runtime deps failed (robocopy exit=$LASTEXITCODE)"
        exit $LASTEXITCODE
    }
} else {
    Write-Host "No .dsh-runtime\node_modules found; exe will fall back to npx."
}

# 4. Bundle a self-contained Node runtime (no Node install needed on target)
$nodeDir = Join-Path $outDir "runtime\node"
$nodeExe = Join-Path $nodeDir "node.exe"
if (-not (Test-Path $nodeExe)) {
    New-Item -ItemType Directory -Force -Path $nodeDir | Out-Null
    $nodeZip = Join-Path $root ".node-download\node.zip"
    New-Item -ItemType Directory -Force -Path (Split-Path $nodeZip) | Out-Null
    if (-not (Test-Path $nodeZip)) {
        $url = "https://npmmirror.com/mirrors/node/v$NodeVersion/node-v$NodeVersion-win-x64.zip"
        Write-Host "Downloading Node v$NodeVersion ..."
        curl.exe -sS -L -o $nodeZip $url
        if ($LASTEXITCODE -ne 0) {
            throw "download Node failed"
        }
    }
    $ext = Join-Path $root ".node-download\extracted"
    Expand-Archive -Path $nodeZip -DestinationPath $ext -Force
    $srcNode = Join-Path $ext "node-v$NodeVersion-win-x64\node.exe"
    if (-not (Test-Path $srcNode)) {
        throw "node.exe not found in archive"
    }
    Copy-Item $srcNode $nodeExe -Force
    Write-Host "Bundled Node v$NodeVersion into bin\runtime\node"
} else {
    Write-Host "Bundled Node already present: $nodeExe"
}

Write-Host "Build done: $out"
Write-Host "Ship bin\dsh-desktop.exe together with bin\runtime."
