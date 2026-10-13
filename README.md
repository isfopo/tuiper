# tuiper

A looper with a terminal UI (Bubble Tea), intended to run against a DAW
(Ableton) via Ableton Link and MIDI. Currently at **Milestone 1: MIDI
monitor** — a live MIDI input monitor showing the port list and an event log.

## Requirements

- **Go** >= 1.24
- **mingw-w64 gcc** for CGO (RtMidi), installed via
  `winget install BrechtSanders.WinLibs.POSIX.UCRT`. `build.ps1` finds it
  automatically (on `PATH` or in the WinGet package directory).
- Optionally **zig** (`winget install zig.zig`) for cross-compiling later;
  use `.\build.ps1 -Zig` to build with it. Note: the zig path is currently
  slow on Windows — each rebuild re-runs the full cgo invocation chain and
  Go's build cache is not reused, so expect ~1–2 minutes per build.

## Build

On Windows:

.\build.ps1            # vet + build tuiper.exe (CGO via mingw gcc)
.\build.ps1 -Watch     # build, then run a headless demo smoke test
.\build.ps1 -Zig       # build with zig cc instead (slow, see above)

This outputs `tuiper.exe` in the repo root. The build script enables CGO,
points `CC`/`CXX` at the mingw gcc compiler, and links the gcc runtimes statically (`CGO_LDFLAGS=-static-libstdc++ -static-libgcc
-static`) so the exe has no mingw DLL dependencies.

## Run

| Command | What it does |
|---|---|
| `.\tuiper.exe` | TUI on real MIDI ports (RtMidi via CGO: WinMM/CoreMIDI/ALSA) |
| `.\tuiper.exe -engine test` | TUI on fake loopback ports (no hardware or CGO needed) |
| `.\tuiper.exe -engine test -demo` | Fake ports plus a synthetic event sender |
| `.\tuiper.exe -watch 5s` | Headless: print events for 5 seconds (smoke test/debugging) |

Flags:

- `-engine rtmidi|test` — MIDI driver. `rtmidi` is the default (real ports);
  `test` uses gomidi `testdrv` loopback ports.
- `-demo` — inject synthetic events (test engine only).
- `-watch <duration>` — headless event printing instead of the TUI.

## TUI keys

- `q` / `esc` / `ctrl+c` — quit
- `↑`/`k`, `↓`/`j` — move selection
- `enter` / `space` — open/close (`toggle`) the selected port
- `r` — refresh the port list

## Project layout

    cmd/tuiper/          entrypoint, flags
    internal/midi/       Engine (driver owner), Monitor (event stream)
    internal/midimon/    Milestone-1 TUI: model.go (state/logic), view.go (render), demo.go
    tools/               zig cc CGO wrappers
    build.ps1            Windows build script

## Roadmap

1. ✅ MIDI monitor (this milestone)
2. Audio I/O: device enumeration, in/out streams, capture/playback round trip
3. Single-channel looper: record/overdub/play/stop/clear, tempo-synced edges
4. MIDI pedal mapping (config file: device, note/CC -> action)
5. Waveform display + position cursor on the TUI
6. Multichannel/track stack, loop-in-loop
7. Ableton Link clock sync (UDP protocol, Go binding or port)
8. Release packaging (prebuilt binaries, `-engine rtmidi` embedding)

See `NOTES.md` for build/packaging notes.
