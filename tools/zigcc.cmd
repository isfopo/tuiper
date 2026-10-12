@echo off
rem CGO wrapper: Go splits CC on spaces, so this shim is needed.
zig cc -target x86_64-windows-gnu %*
