# tuiper development/build script
# Usage:  .\build.ps1          -> build to tuiper.exe
#         .\build.ps1 -Watch   -> also run `-engine test -demo -watch 6s` smoke test
param(
    [switch]$Watch
)

$ErrorActionPreference = 'Stop'

# CGO is required by rtmididrv (RtMidi). Go splits CC/CXX on spaces, so we
# wrap zig (installed via winget: zig.zig) in tiny cmd shims under tools/.
$env:CGO_ENABLED = '1'
$env:CC  = "$PSScriptRoot\tools\zigcc.cmd"
$env:CXX = "$PSScriptRoot\tools\zigcxx.cmd"

go vet ./...
go build -o tuiper.exe ./cmd/tuiper
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "built tuiper.exe (CGO via zig)" -ForegroundColor Green

if ($Watch) {
    .\tuiper.exe -engine test -demo -watch 6s
}
