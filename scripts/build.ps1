#requires -Version 5.1
<#
.SYNOPSIS
  Build Tash binaries and installers for every supported target.

.DESCRIPTION
  Produces self-contained binaries and terminal installers. Windows and Linux
  installers can be cross-built from any host. macOS DMGs are only created on
  macOS (hdiutil); other hosts still build the raw macOS installer binary.

.EXAMPLE
  ./scripts/build.ps1
  ./scripts/build.ps1 -Targets windows,linux/arm64
  ./scripts/build.ps1 -Version 1.2.0 -SkipInstallers
#>
param(
    [string]$Version = "",
    [string]$OutDir = "dist",
    [string]$TempDir = "TEMP",
    [string[]]$Targets = @(),
    [switch]$SkipInstallers,
    [switch]$SkipMacDMG,
    [switch]$Clean
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

function Resolve-Go {
    if ($env:GO -and (Test-Path $env:GO)) { return $env:GO }
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $fallbacks = @(
        (Join-Path $env:LOCALAPPDATA "Programs\Go\bin\go.exe"),
        "C:\Program Files\Go\bin\go.exe",
        "/usr/local/go/bin/go",
        "/opt/homebrew/bin/go"
    )
    foreach ($p in $fallbacks) {
        if ($p -and (Test-Path $p)) { return $p }
    }
    throw "Go was not found. Install Go or set the GO environment variable to the go executable."
}

function Safe-Version([string]$v) {
    $s = $v.Trim()
    if ($s -eq "") { $s = "0.20-dev" }
    return ($s -replace '[^A-Za-z0-9._+-]', '-')
}

$Go = Resolve-Go
$OutDir = Join-Path $Root $OutDir
$TempOut = Join-Path $Root $TempDir

function FriendlyOS([string]$osName) {
    switch ($osName) {
        "darwin" { return "mac" }
        default { return $osName }
    }
}

function FriendlyArch([string]$osName, [string]$arch) {
    if ($osName -eq "darwin") {
        if ($arch -eq "amd64") { return "intel" }
        if ($arch -eq "arm64") { return "apple-silicon" }
    }
    if (($osName -eq "windows" -or $osName -eq "linux") -and $arch -eq "amd64") {
        return "x64"
    }
    return $arch
}

function InstallerFileName([string]$version, [string]$osName, [string]$arch) {
    $base = "Tash-Setup-$version-$(FriendlyOS $osName)-$(FriendlyArch $osName $arch)"
    switch ($osName) {
        "windows" { return $base + ".exe" }
        "linux" { return $base + ".elf" }
        default { return $base }
    }
}

function PortableFileName([string]$version, [string]$osName, [string]$arch) {
    $base = "Tash-Portable-$version-$(FriendlyOS $osName)-$(FriendlyArch $osName $arch)"
    if ($osName -eq "windows") { return $base + ".exe" }
    return $base
}

if ($Version -eq "") {
    try { $Version = (& git describe --tags --always --dirty 2>$null | Select-Object -First 1) } catch { $Version = "" }
}
if (-not $Version) { $Version = "0.20-dev" }
$Version = $Version.Trim()
$FileVersion = Safe-Version $Version

$Commit = "local"
try {
    $c = (& git rev-parse --short HEAD 2>$null | Select-Object -First 1)
    if ($c) { $Commit = $c.Trim() }
} catch { }

$BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$LdFlags = "-s -w -X github.com/tash/tash/internal/version.Version=$Version " +
           "-X github.com/tash/tash/internal/version.Commit=$Commit " +
           "-X github.com/tash/tash/internal/version.BuildDate=$BuildDate"

$AllTargets = @(
    "windows/amd64", "windows/arm64",
    "linux/amd64", "linux/arm64",
    "darwin/amd64", "darwin/arm64"
)

if ($Targets.Count -gt 0) {
    $expanded = @()
    foreach ($entry in $Targets) {
        foreach ($part in ($entry -split ",")) {
            $p = $part.Trim().ToLower()
            if ($p -eq "") { continue }
            if ($p.Contains("/")) {
                $expanded += $p
            } else {
                $expanded += $AllTargets | Where-Object { $_ -like "$p/*" }
            }
        }
    }
    $AllTargets = $AllTargets | Where-Object { $expanded -contains $_ } | Select-Object -Unique
}

if ($AllTargets.Count -eq 0) {
    throw "No matching targets. Use windows, linux, darwin, or os/arch (for example linux/arm64)."
}

if ($Clean -and (Test-Path $OutDir)) {
    Remove-Item -LiteralPath $OutDir -Recurse -Force
}
if ($Clean -and (Test-Path $TempOut)) {
    Remove-Item -LiteralPath $TempOut -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
New-Item -ItemType Directory -Force -Path $TempOut | Out-Null

$IsMacHost = $false
try {
    $IsMacHost = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
        [System.Runtime.InteropServices.OSPlatform]::OSX)
} catch { }

$Payload = Join-Path $Root "cmd/installer/payload/tash_payload"
$Built = @()

function Invoke-GoBuild([string]$osName, [string]$arch, [string]$output, [string]$package) {
    $oldGOOS = $env:GOOS
    $oldGOARCH = $env:GOARCH
    $oldCGO = $env:CGO_ENABLED
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = $osName
        $env:GOARCH = $arch
        & $Go build -trimpath -ldflags $LdFlags -o $output $package
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed for $osName/$arch ($package)"
        }
    } finally {
        $env:GOOS = $oldGOOS
        $env:GOARCH = $oldGOARCH
        $env:CGO_ENABLED = $oldCGO
    }
}

Write-Host ""
Write-Host "TASH RELEASE BUILD" -ForegroundColor Green
Write-Host "  version: $Version"
Write-Host "  commit:  $Commit"
Write-Host "  go:      $Go"
Write-Host "  out:     $OutDir"
Write-Host "  targets: $($AllTargets -join ', ')"
Write-Host ""

foreach ($target in $AllTargets) {
    $parts = $target.Split("/")
    $osName = $parts[0]
    $arch = $parts[1]
    $ext = if ($osName -eq "windows") { ".exe" } else { "" }
    $tashOut = Join-Path $OutDir "tash_${FileVersion}_${osName}_${arch}${ext}"

    Write-Host "==> tash $target" -ForegroundColor Cyan
    Invoke-GoBuild $osName $arch $tashOut "./cmd/tash"
    $Built += $tashOut
    Copy-Item -LiteralPath $tashOut -Destination (Join-Path $TempOut (PortableFileName $FileVersion $osName $arch)) -Force

    if ($SkipInstallers) { continue }

    $installerName = "Tash-Setup-$FileVersion-$osName-$arch"
    if ($osName -eq "windows") { $installerName += ".exe" }
    elseif ($osName -eq "linux") { $installerName += ".elf" }
    $installerOut = Join-Path $OutDir $installerName

    Write-Host "==> installer $target" -ForegroundColor Cyan
    Copy-Item -LiteralPath $tashOut -Destination $Payload -Force
    try {
        Invoke-GoBuild $osName $arch $installerOut "./cmd/installer"
    } finally {
        Remove-Item -LiteralPath $Payload -Force -ErrorAction SilentlyContinue
    }
    $Built += $installerOut
    Copy-Item -LiteralPath $installerOut -Destination (Join-Path $TempOut (InstallerFileName $FileVersion $osName $arch)) -Force
}

if (-not $SkipInstallers -and -not $SkipMacDMG) {
    $darwinTargets = $AllTargets | Where-Object { $_ -like "darwin/*" }
    if ($darwinTargets.Count -gt 0 -and -not $IsMacHost) {
        Write-Host "==> macOS DMG skipped: hdiutil is only available on macOS" -ForegroundColor Yellow
    }
    if ($IsMacHost) {
        foreach ($target in $darwinTargets) {
            $arch = $target.Split("/")[1]
            $installer = Join-Path $OutDir "Tash-Setup-$FileVersion-darwin-$arch"
            if (-not (Test-Path $installer)) { continue }
            $stage = Join-Path $OutDir ".dmg-$arch"
            if (Test-Path $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
            New-Item -ItemType Directory -Force -Path $stage | Out-Null
            Copy-Item -LiteralPath $installer -Destination (Join-Path $stage "tash-installer")
            & chmod +x (Join-Path $stage "tash-installer")

            $command = @'
#!/bin/bash
# Double-click this file to open Terminal and install Tash.
cd "$(dirname "$0")" || exit 1
chmod +x ./tash-installer
exec ./tash-installer "$@"
'@
            $commandPath = Join-Path $stage "Install Tash.command"
            $utf8 = New-Object System.Text.UTF8Encoding($false)
            [IO.File]::WriteAllText($commandPath, $command.Replace("`r`n", "`n"), $utf8)
            & chmod +x $commandPath

            $readme = @'
TASH FOR MACOS
==============

1. Double-click "Install Tash.command".
   Terminal opens automatically. The installer copies tash to ~/.local/bin,
   adds it to PATH, and starts first-run setup.

2. Choose MAIN DEVICE for the first machine. Choose JOIN TASHNET for every
   other machine and paste the Tashnet Secret Key. The main device must
   approve the request in its TASHNET tab.

If macOS blocks the unsigned installer, right-click it and choose Open, or run:

  xattr -d com.apple.quarantine "Install Tash.command"

Then run `tash dashboard` from any new terminal.

The DMG is architecture-specific. Use the arm64 DMG on Apple Silicon and the
amd64 DMG on Intel Macs. The Tash binary is self-contained; no root is needed.
'@
            [IO.File]::WriteAllText((Join-Path $stage "README.txt"), $readme.Replace("`r`n", "`n"), $utf8)

            $dmg = Join-Path $OutDir "Tash-Setup-$FileVersion-macos-$arch.dmg"
            Write-Host "==> DMG darwin/$arch" -ForegroundColor Cyan
            & hdiutil create -volname "Tash" -srcfolder $stage -ov -format UDZO $dmg | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "hdiutil failed for $arch" }
            $Built += $dmg
            $dmgFriendly = "Tash-Setup-$FileVersion-mac-$(FriendlyArch 'darwin' $arch).dmg"
            Copy-Item -LiteralPath $dmg -Destination (Join-Path $TempOut $dmgFriendly) -Force
            Remove-Item -LiteralPath $stage -Recurse -Force
        }
    }
}

$checksumPath = Join-Path $OutDir "checksums.txt"
$checksumFiles = Get-ChildItem -LiteralPath $OutDir -File |
    Where-Object { $_.Name -ne "checksums.txt" } |
    Sort-Object Name
$lines = foreach ($f in $checksumFiles) {
    $hash = (Get-FileHash -LiteralPath $f.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $($f.Name)"
}
$utf8 = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText($checksumPath, (($lines -join "`n") + "`n"), $utf8)

$tempReadme = @'
TASH ARTIFACTS
==============

Installers (run from a terminal; they prompt for the install directory and
offer to install the Go toolchain):

  Windows x64          Tash-Setup-<version>-windows-x64.exe
  Windows arm64        Tash-Setup-<version>-windows-arm64.exe
  Linux x64            Tash-Setup-<version>-linux-x64.elf
  Linux arm64          Tash-Setup-<version>-linux-arm64.elf
  macOS Intel          Tash-Setup-<version>-mac-intel
  macOS Apple Silicon  Tash-Setup-<version>-mac-apple-silicon

Portable builds (one self-contained executable; no installer, no PATH changes):

  Tash-Portable-<version>-windows-x64.exe
  Tash-Portable-<version>-windows-arm64.exe
  Tash-Portable-<version>-linux-x64
  Tash-Portable-<version>-linux-arm64
  Tash-Portable-<version>-mac-intel
  Tash-Portable-<version>-mac-apple-silicon

Notes
-----
- The macOS DMGs (Tash-Setup-<version>-mac-intel.dmg and
  Tash-Setup-<version>-mac-apple-silicon.dmg) are created on a macOS host with
  hdiutil. If they are missing here, run the build on macOS or use CI.
- The macOS installer binaries are unsigned; right-click -> Open if Gatekeeper
  blocks them.
- Portable binaries can be run directly: `tash dashboard`.
- Installers support --prefix DIR, --portable, --with-go, --no-path,
  --no-launch, --yes, and --dry-run.
'@
[IO.File]::WriteAllText((Join-Path $TempOut "README.txt"), $tempReadme.Replace("`r`n", "`n"), $utf8)

$tempChecksumPath = Join-Path $TempOut "checksums.txt"
$tempFiles = Get-ChildItem -LiteralPath $TempOut -File |
    Where-Object { $_.Name -ne "checksums.txt" } |
    Sort-Object Name
$tempLines = foreach ($f in $tempFiles) {
    $hash = (Get-FileHash -LiteralPath $f.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $($f.Name)"
}
[IO.File]::WriteAllText($tempChecksumPath, (($tempLines -join "`n") + "`n"), $utf8)

Write-Host ""
Write-Host "Build complete." -ForegroundColor Green
foreach ($f in $checksumFiles) {
    $size = "{0:N1} MiB" -f ((Get-Item -LiteralPath $f.FullName).Length / 1MB)
    Write-Host ("  {0,-52} {1,10}" -f $f.Name, $size)
}
Write-Host "  checksums: $checksumPath"
Write-Host "  TEMP:      $TempOut"
Write-Host ""
