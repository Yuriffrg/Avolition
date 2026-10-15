<# Build the portable Avolition folder using an official Electron Windows x64 archive. #>
[CmdletBinding()]
param(
    [string]$Python = 'python',
    [string]$DependencyPath = '',
    [string]$BuildRoot = '',
    [string]$ElectronArchive = '',
    [switch]$CheckOnly,
    # The Go core (gocore/) is the backend and needs Go on PATH; Python is then
    # used only by the packaging script (and PyInstaller's icon/version helper).
    # -PythonCore builds the legacy PyInstaller-frozen app.py backend instead.
    [switch]$PythonCore,
    # Accepted for compatibility with 0.8.22-era instructions; Go is the default.
    [switch]$GoCore
)
Set-StrictMode -Version Latest
$taskUseGo = -not $PythonCore
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
import importlib.metadata as m, struct, sys
assert struct.calcsize('P') == 8, '64-bit Python required'
if sys.argv[1] != 'go':
    assert m.version('pillow') == '12.3.0', 'Install requirements-build.txt'
    assert m.version('numpy') == '2.3.5', 'Install requirements-build.txt'
assert m.version('pyinstaller') == '6.22.3', 'Install requirements-build.txt'
print('64-bit Python and PyInstaller 6.22.3 ready')
'@
    $taskBackend = if ($taskUseGo) { 'go' } else { 'python' }
    & $taskPython -c $taskCheck $taskBackend
    if ($LASTEXITCODE -ne 0) { throw 'Build dependency check failed.' }
    if ($taskUseGo) {
        $taskGo = (Get-Command 'go' -ErrorAction Stop).Source
        & $taskGo version
        if ($LASTEXITCODE -ne 0) { throw 'Go toolchain check failed.' }
    }
    if ($CheckOnly) { return }
    if (-not $taskUseGo) {
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
    }
    $taskPackageArguments = @((Join-Path $taskSourceRoot 'tools\package_desktop.py'), '--source', $taskSourceRoot,
        '--build-root', $taskBuildRoot, '--electron', $taskElectronArchive)
    if ($taskUseGo) {
        $taskGoCore = Join-Path $taskBuildRoot 'gocore\owio-core.exe'
        Push-Location (Join-Path $taskSourceRoot 'gocore')
        try {
            $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
            & $taskGo build -trimpath -ldflags '-s -w' -o $taskGoCore ./cmd/owio-core
            if ($LASTEXITCODE -ne 0) { throw 'Avolition Go core build failed.' }
        } finally {
            Pop-Location
            Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
        }
        $taskPackageArguments += @('--go-core', $taskGoCore)
    }
    & $taskPython @taskPackageArguments
    if ($LASTEXITCODE -ne 0) { throw 'Avolition desktop packaging failed.' }
    Write-Host ('Portable app: ' + (Join-Path $taskBuildRoot 'dist\Avolition\Avolition.exe'))
    Write-Host 'Distribute the entire Avolition folder; keep its runtime files beside Avolition.exe.'
} finally {
    foreach ($taskKey in $taskEnvironmentNames) {
        [Environment]::SetEnvironmentVariable($taskKey, $taskSavedEnvironment[$taskKey], 'Process')
    }
}
