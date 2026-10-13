# tuiper

A looper with a terminal UI (Bubble Tea), intended to run against a DAW
(Ableton) via Ableton Link and MIDI.

## Current status — Milestone 1: MIDI monitor

- Bubble Tea MIDI monitor: port list, open/close, live event log.
- Engines:
  - `rtmidi` (default): real ports via RtMidi/CGO (WinMM/CoreMIDI/ALSA).
  - `test`: fake loopback ports via gomidi `testdrv`, no CGO in the path.
- `-demo`: synthetic event sender on the test engine.
- `-watch 5s`: headless event printing (smoke test / debugging).

## Build (Windows)

Requires: Go >= 1.24, mingw-w64 gcc
(`winget install BrechtSanders.WinLibs.POSIX.UCRT`).

    .\build.ps1            # vet + build tuiper.exe (CGO via mingw gcc)
    .\build.ps1 -Watch     # also run the headless demo smoke test
    .\build.ps1 -Zig       # build with zig cc instead (slow on Windows)

gcc runtimes are linked statically so the exe is self-contained. The zig
path (tools/zigcc.cmd / tools/zigcxx.cmd shims) still works but is kept
mainly for cross-compiling release targets later.

TUI development now happens in a Linux container (Dockerfile +
docker-compose.yml; Go caches in named volumes, test engine for the TUI,
ALSA via the `midi` profile on Linux hosts). Windows is the host-side path:
real-hardware MIDI testing and release binaries.

    .\tuiper.exe                       # TUI on real ports
    .\tuiper.exe -engine test -demo    # TUI on fake loopback + synthetic events

## Packaging notes (kept in mind for later)

- `tools/zigcc.cmd` / `tools/zigcxx.cmd` are the only nonstandard build
  machinery for the zig path; a release build can run `go build` with CGO=1
  and `-Zig` (or gcc equivalents per host). zig is also a native cross
  compiler, so Windows/macOS/Linux release targets can come from one machine
  eventually.
- The `test` engine proves a workflow with no hardware/toolchain lock-in.

## Roadmap (rough order)

1. x  MIDI monitor (this milestone)
2. Audio I/O: device enumeration, in/out streams, capture playback round trip
3. Single-channel looper: record/overdub/play/stop/clear, tempo-synced edges
4. MIDI pedal mapping (config file: device, note/CC -> action)
5. Waveform display + position cursor on the TUI
6. Multichannel/track stack, loop-in-loop
7. Ableton Link clock sync (UDP protocol, Go binding or port)
8. Release packaging (prebuilt binaries, `-engine rtmidi` embedding)

## Layout

    cmd/tuiper/          entrypoint, flags
    internal/midi/       Engine (driver owner), Monitor (event stream)
    internal/midimon/    Milestone-1 TUI: model.go (state/logic), view.go (render), demo.go
    tools/               zig cc CGO wrappers
