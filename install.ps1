# Spore Code — one-liner installer for Windows.
#
#   irm https://raw.githubusercontent.com/yumlevi/spore-code/main/install.ps1 | iex
#
# Optional overrides (set before the pipe):
#   $env:SPORE_CODE_VERSION = 'v1.0.0'  # pin a specific release tag
#   $env:SPORE_CODE_DIR     = 'C:\tools' # install to a different directory
#
# Re-running upgrades in place. Same script handles install + upgrade.

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo    = 'yumlevi/spore-code'
$Version = if ($env:SPORE_CODE_VERSION) { $env:SPORE_CODE_VERSION } else { 'latest' }
$BinName = 'spore.exe'

function Write-Step([string]$msg) { Write-Host "→ $msg" -ForegroundColor Cyan }
function Write-Ok  ([string]$msg) { Write-Host "✓ $msg" -ForegroundColor Green }
function Write-Hint([string]$msg) { Write-Host "  $msg" -ForegroundColor DarkGray }
function Die       ([string]$msg) { Write-Host "✗ $msg" -ForegroundColor Red; exit 1 }

# ── arch detection (PROCESSOR_ARCHITECTURE is reliable on every Win SKU) ──
switch -regex ($env:PROCESSOR_ARCHITECTURE) {
  '^(AMD64|x86_64)$' { $arch = 'amd64' }
  '^ARM64$'          { $arch = 'arm64' }
  default            { Die "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

# ── resolve `latest` to a real tag so we can show it to the user ──
if ($Version -eq 'latest') {
  try {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" `
      -Headers @{ 'User-Agent' = 'spore-code-installer' }
    if ($rel.tag_name) { $Version = $rel.tag_name }
  } catch {
    Write-Hint "Could not resolve 'latest' tag — will use the latest-redirect URL anyway."
  }
}

# ── pick install dir ──
if ($env:SPORE_CODE_DIR) {
  $DestDir = $env:SPORE_CODE_DIR
} else {
  $DestDir = Join-Path $env:USERPROFILE '.spore-code\bin'
}
[void](New-Item -ItemType Directory -Path $DestDir -Force)
$DestPath = Join-Path $DestDir $BinName

$AssetUrl = if ($Version -eq 'latest') {
  "https://github.com/$Repo/releases/latest/download/spore-windows-$arch.exe"
} else {
  "https://github.com/$Repo/releases/download/$Version/spore-windows-$arch.exe"
}

# ── download → temp → verify → atomic move ──
$Tmp = Join-Path $env:TEMP ("spore-" + [Guid]::NewGuid().ToString('N') + '.exe')

Write-Step "Downloading spore $Version for windows/$arch"
Write-Hint $AssetUrl

try {
  Invoke-WebRequest -Uri $AssetUrl -OutFile $Tmp -UseBasicParsing -Headers @{ 'User-Agent' = 'spore-code-installer' }
} catch {
  Die "Download failed: $($_.Exception.Message)"
}

# Verify the file actually starts with the PE 'MZ' magic (catches HTML 404s).
$head = [byte[]]::new(2)
$fs = [IO.File]::OpenRead($Tmp)
try { [void]$fs.Read($head, 0, 2) } finally { $fs.Close() }
if (-not ($head[0] -eq 0x4D -and $head[1] -eq 0x5A)) {
  Remove-Item $Tmp -Force -ErrorAction SilentlyContinue
  Die "Downloaded file isn't a Windows binary (asset missing for this platform?)"
}

# If a previous spore.exe is in place, Windows can't replace a running
# image — rename it aside first. Falls back to a stale .old file the
# user can delete next reboot.
if (Test-Path $DestPath) {
  Write-Step "Replacing existing $DestPath"
  $Backup = "$DestPath.old"
  if (Test-Path $Backup) { Remove-Item $Backup -Force -ErrorAction SilentlyContinue }
  try {
    Move-Item $DestPath $Backup -Force
  } catch {
    Die "Couldn't move the existing spore.exe aside (is it open in another terminal? close spore and retry)."
  }
}

try {
  Move-Item $Tmp $DestPath -Force
} catch {
  Die "Could not write $DestPath ($($_.Exception.Message))"
}

Write-Ok "Installed to $DestPath"

# ── PATH advice — automatically add to per-user PATH if missing ──
$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $UserPath) { $UserPath = '' }
$onPath = ($UserPath -split ';' | Where-Object { $_ -ieq $DestDir }).Count -gt 0

if (-not $onPath) {
  $newPath = if ($UserPath.TrimEnd(';')) { "$($UserPath.TrimEnd(';'));$DestDir" } else { $DestDir }
  try {
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Ok "Added $DestDir to your user PATH"
    Write-Hint "Open a new terminal for the change to take effect."
  } catch {
    Write-Hint "$DestDir is not in your PATH and we couldn't add it automatically."
    Write-Hint "Add it manually: System Properties → Environment Variables → User Path."
  }
}

Write-Host ""
Write-Host "Run " -NoNewline -ForegroundColor DarkGray
Write-Host "spore" -NoNewline -ForegroundColor White
Write-Host " in a new terminal to start. First launch walks you through setup." -ForegroundColor DarkGray
