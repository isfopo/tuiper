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

Requires: Go >= 1.24, zig (`winget install zig.zig`).

    .\build.ps1            # vet + build tuiper.exe (CGO via zig cc)
    .\build.ps1 -Watch     # also run the headless demo smoke test

    .\tuiper.exe                       # TUI on real ports
    .\tuiper.exe -engine test -demo    # TUI on fake loopback + synthetic events

## Packaging notes (kept in mind for later)

- `tools/zigcc.cmd` / `tools/zigcxx.cmd` are the only nonstandard build
  machinery; a release build can run `go build` with CGO=1 and these CC shims
  in CI. zig is also a native cross compiler, so Windows/macOS/Linux release
  targets can come from one machine eventually.
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
