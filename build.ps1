<# Build the portable Avolition folder using an official Electron Windows x64 archive. #>
[CmdletBinding()]
param(
    [string]$Python = 'python',
    [string]$DependencyPath = '',
    [string]$BuildRoot = '',
    [string]$ElectronArchive = '',
    [switch]$CheckOnly
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Build Avolition on Windows using 64-bit Python.' }
$taskSourceRoot = [IO.Path]::GetFullPath($PSScriptRoot)
if (-not $BuildRoot) { $BuildRoot = Join-Path $taskSourceRoot '.build' }
$taskBuildRoot = [IO.Path]::GetFullPath($BuildRoot)
if ($taskBuildRoot -eq [IO.Path]::GetPathRoot($taskBuildRoot) -or $taskBuildRoot -eq $taskSourceRoot) {
    throw 'BuildRoot must be a dedicated build directory.'
}
if (-not $ElectronArchive) { throw 'Provide -ElectronArchive electron-v44.4.3-win32-x64.zip.' }
$taskElectronArchive = (Resolve-Path -LiteralPath $ElectronArchive).Path
$taskPython = (Get-Command $Python -ErrorAction Stop).Source
if ($DependencyPath) { $DependencyPath = (Resolve-Path -LiteralPath $DependencyPath).Path }
$taskEnvironmentNames = @('PYTHONPATH', 'PYTHONNOUSERSITE', 'PYTHONDONTWRITEBYTECODE', 'PYINSTALLER_CONFIG_DIR', 'TEMP', 'TMP')
$taskSavedEnvironment = @{}
foreach ($taskKey in $taskEnvironmentNames) {
    $taskSavedEnvironment[$taskKey] = [Environment]::GetEnvironmentVariable($taskKey, 'Process')
}
try {
    foreach ($taskFolder in @('dist', 'core', 'work', 'spec', 'cache', 'temp')) {
        New-Item -ItemType Directory -Path (Join-Path $taskBuildRoot $taskFolder) -Force | Out-Null
    }
    if ($DependencyPath) { $env:PYTHONPATH = $DependencyPath }
    $env:PYTHONNOUSERSITE = '1'
    $env:PYTHONDONTWRITEBYTECODE = '1'
    $env:PYINSTALLER_CONFIG_DIR = Join-Path $taskBuildRoot 'cache'
    $env:TEMP = Join-Path $taskBuildRoot 'temp'
    $env:TMP = $env:TEMP
    $taskCheck = @'
import importlib.metadata as m, struct
assert struct.calcsize('P') == 8, '64-bit Python required'
assert m.version('pillow') == '12.3.0', 'Install requirements-build.txt'
assert m.version('numpy') == '2.3.5', 'Install requirements-build.txt'
assert m.version('pyinstaller') == '6.22.3', 'Install requirements-build.txt'
print('64-bit Python and PyInstaller 6.22.3 ready')
'@
    & $taskPython -c $taskCheck
    if ($LASTEXITCODE -ne 0) { throw 'Build dependency check failed.' }
    if ($CheckOnly) { return }
    $taskCore = [IO.Path]::GetFullPath((Join-Path $taskBuildRoot 'core\owio-core'))
    if (-not $taskCore.StartsWith($taskBuildRoot.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Core path escapes BuildRoot.'
    }
    $taskArguments = @('-m', 'PyInstaller', '--noconfirm', '--clean', '--onedir', '--console', '--noupx',
        '--name', 'owio-core', '--distpath', (Join-Path $taskBuildRoot 'core'),
        '--workpath', (Join-Path $taskBuildRoot 'work'), '--specpath', (Join-Path $taskBuildRoot 'spec'),
        '--paths', $taskSourceRoot)
    if ($DependencyPath) { $taskArguments += @('--paths', $DependencyPath) }
    $taskArguments += (Join-Path $taskSourceRoot 'app.py')
    & $taskPython @taskArguments
    if ($LASTEXITCODE -ne 0) { throw 'Avolition backend build failed.' }
    & $taskPython (Join-Path $taskSourceRoot 'tools\package_desktop.py') --source $taskSourceRoot --build-root $taskBuildRoot --electron $taskElectronArchive
    if ($LASTEXITCODE -ne 0) { throw 'Avolition desktop packaging failed.' }
    Write-Host ('Portable app: ' + (Join-Path $taskBuildRoot 'dist\Avolition\Avolition.exe'))
    Write-Host 'Distribute the entire Avolition folder; keep its runtime files beside Avolition.exe.'
} finally {
    foreach ($taskKey in $taskEnvironmentNames) {
        [Environment]::SetEnvironmentVariable($taskKey, $taskSavedEnvironment[$taskKey], 'Process')
    }
}
