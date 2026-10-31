# sys1 installer for Windows.
#
#   irm https://a-hackers-guide.github.io/sys1/install.ps1 | iex
#
# Installs sys0 (client) and sys1 (node) into %LOCALAPPDATA%\Programs\sys1
# and adds it to your PATH, after verifying the SHA-256 checksum.
$ErrorActionPreference = 'Stop'
$repo = 'A-Hackers-Guide/sys1'

$arch = if ([Environment]::Is64BitOperatingSystem) {
  if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
} else { throw 'sys1 needs 64-bit Windows.' }

$release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name
$num = $version.TrimStart('v')
$file = "sys1_${num}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$version"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("sys1-" + [Guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
  Write-Host "  Downloading sys1 $version for windows/$arch..."
  Invoke-WebRequest "$base/$file" -OutFile "$tmp\$file" -UseBasicParsing
  Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

  $want = (Select-String -Path "$tmp\checksums.txt" -Pattern " $([regex]::Escape($file))$").Line.Split(' ')[0]
  $got = (Get-FileHash "$tmp\$file" -Algorithm SHA256).Hash.ToLower()
  if (-not $want -or $want -ne $got) { throw 'Checksum mismatch; the download is corrupt or was tampered with.' }
  Write-Host '  Checksum verified.'

  $dest = Join-Path $env:LOCALAPPDATA 'Programs\sys1'
  New-Item -ItemType Directory -Force $dest | Out-Null
  Expand-Archive "$tmp\$file" -DestinationPath $tmp -Force
  Copy-Item "$tmp\sys0.exe", "$tmp\sys1.exe" $dest -Force

  $path = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($path -split ';') -notcontains $dest) {
    [Environment]::SetEnvironmentVariable('Path', "$path;$dest", 'User')
    Write-Host "  Added $dest to your PATH (open a new terminal to use it)."
  }
  Write-Host "  Installed sys0 and sys1 to $dest"
  Write-Host ''
  Write-Host '  Lend this PC to a pool:  sys0   (run as Administrator to install the service)'
  Write-Host '  Run a node here:         sys1'
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
