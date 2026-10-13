# tuiper development/build script
# Usage:  .\build.ps1           -> build to tuiper.exe
#         .\build.ps1 -Watch    -> also run `-engine test -demo -watch 6s` smoke test
#         .\build.ps1 -Zig      -> build with zig cc instead of mingw gcc
#                                  (slow on Windows right now: each rebuild
#                                  re-runs ~150 cgo invocations; kept for
#                                  cross-compiling release targets later)
param(
    [switch]$Watch,
    [switch]$Zig
)

echo "Starting build..."

$ErrorActionPreference = 'Stop'

# CGO is required by rtmididrv (RtMidi).
$env:CGO_ENABLED = '1'

# Link the gcc runtimes statically so the exe has no mingw DLLs to find at runtime.
$env:CGO_LDFLAGS = '-static-libstdc++ -static-libgcc -static'

if ($Zig) {
    # Go splits CC/CXX on spaces, so zig (installed via winget: zig.zig)
    # is wrapped in tiny cmd shims under tools/.
    $env:CC  = "$PSScriptRoot\tools\zigcc.cmd"
    $env:CXX = "$PSScriptRoot\tools\zigcxx.cmd"
    Write-Host "compiler: zig cc (slow)" -ForegroundColor Yellow
} else {
    # Default: mingw-w64 gcc (winget install BrechtSanders.WinLibs.POSIX.UCRT).
    # On PATH after a shell restart?
    $gcc = Get-Command gcc.exe -ErrorAction SilentlyContinue
    if ($gcc -and ((Split-Path $gcc.Source -Leaf) -eq 'gcc.exe')) {
        $bin = Split-Path $gcc.Source
        $env:CC  = Join-Path $bin 'gcc.exe'
        $env:CXX = Join-Path $bin 'g++.exe'
        Write-Host "compiler: mingw gcc ($env:CC)" -ForegroundColor Gray
    } else {
        # Fall back to the WinGet package location.
        $pkgRoot = "$env:LOCALAPPDATA\Microsoft\WinGet\Packages"
        $bin = Get-ChildItem $pkgRoot -Recurse -Filter 'gcc.exe' -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match 'mingw64\\bin' } |
            Select-Object -First 1
        if ($bin) {
            $env:CC  = $bin.FullName
            $gpp = Get-ChildItem (Split-Path $bin.FullName) -Filter 'g++.exe' | Select-Object -First 1
            $env:CXX = $gpp.FullName
            Write-Host "compiler: mingw gcc ($env:CC)" -ForegroundColor Gray
        } else {
            Write-Error "no gcc found. Install with: winget install BrechtSanders.WinLibs.POSIX.UCRT (or use -Zig)"
            exit 1
        }
    }
}

go vet ./...
go build -o tuiper.exe ./cmd/tuiper
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "built tuiper.exe" -ForegroundColor Green

if ($Watch) {
    .\tuiper.exe -engine test -demo -watch 6s
}
