@echo off
rem CGO wrapper for C++ (RtMidi). Go splits CXX on spaces, so this shim is needed.
zig c++ -target x86_64-windows-gnu %*
