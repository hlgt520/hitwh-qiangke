# 一键发布脚本：构建 Windows 单文件 exe（386 + amd64）并打包 zip 到 dist/
# 用法：
#   .\build-release.ps1                  # 版本号自动取 git tag（无 tag 用 commit hash）
#   .\build-release.ps1 -Version v1.0.0  # 手动指定版本号
param(
    [string]$Version = ""
)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not $Version) {
    $v = git describe --tags --always 2>$null
    if (-not $v) { $v = "dev-" + (Get-Date -Format "yyyyMMdd") }
    $Version = $v.Trim()
}

$dist = Join-Path $PSScriptRoot "dist"
$tmp  = Join-Path $dist "_tmp"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

$env:GOOS = "windows"
foreach ($arch in @("386", "amd64")) {
    Write-Host "==> 构建 windows/$arch ..."
    $env:GOARCH = $arch
    go build -trimpath -ldflags "-s -w" -o (Join-Path $tmp "qiangke.exe") .
    if ($LASTEXITCODE -ne 0) { throw "windows/$arch 构建失败" }

    $pkgDir = Join-Path $dist ("qiangke-" + $Version + "-windows-" + $arch)
    New-Item -ItemType Directory -Force -Path $pkgDir | Out-Null
    Copy-Item (Join-Path $tmp "qiangke.exe") $pkgDir
    Copy-Item (Join-Path $PSScriptRoot "部署说明.txt") $pkgDir
    Copy-Item (Join-Path $PSScriptRoot "account.example.json") $pkgDir
    Copy-Item (Join-Path $PSScriptRoot "README.md") $pkgDir

    $zip = $pkgDir + ".zip"
    Compress-Archive -Path ($pkgDir + "\*") -DestinationPath $zip -Force
    Remove-Item $pkgDir -Recurse -Force
    Write-Host "OK: $zip"
}

Remove-Item $tmp -Recurse -Force
Write-Host "全部完成，产物在 dist\ 目录"
