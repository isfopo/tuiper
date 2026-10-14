# tuiper — midi + audio app template

A hardened starting point for terminal applications that need **MIDI and
audio I/O**. Grew out of the tuiper looper; this branch keeps only the
foundation — the two IO packages, a Bubble Tea shell with a device-selection
modal, a level meter — and deliberately strips the looper (the looper itself
lives on the `master` branch).

```
◉ tuiper template  midi + audio io                    ○ ○

midi in : tuiper-test-in ●
audio in: system default  [####-------------] -6.2dBFS 48kf/s
audio out: system default

[audio:live]  audio capture: system default (2 ch)
q quit • d devices • m monitor • r refresh • esc quit • ● midi activity
```

- `d` opens the **device modal** (github.com/sraaaaaaay/bubbletea-modal, over
  the dimmed main frame): four sections — *midi in*, *audio in*, *audio out*,
  *channels* — `tab`/`shift+tab` switch, `↑/↓` move, `enter` applies
  (opening a port, re-opening streams), `esc` closes. Applying a capture
  device starts capture immediately; the main UI mirrors what was selected.
- `m` toggles the monitor path (capture → playback; on real hardware
  mic→speakers is a live feedback loop — the container rig makes it safe).
- The main UI stays minimal on purpose: selected-I/O summary + capture meter
  only. Counter fields for received MIDI events remain in the model for
  applications to build on.

## Running it

| Command | What it does |
|---|---|
| `.\tuiper.exe` | TUI on real MIDI ports (RtMidi via CGO: WinMM/CoreMIDI/ALSA) + real audio |
| `.\tuiper.exe -engine test` | TUI on fake MIDI loopback ports (no hardware needed) |
| `.\tuiper.exe -engine test -demo` | Fake ports plus a synthetic event sender (lamps blink) |
| `.\tuiper.exe -watch 5s` | Headless: print MIDI events for 5 seconds (smoke test) |
| `.\tuiper.exe -audio 5s` | Headless audio: enumerate devices, meter default capture |
| `.\tuiper.exe -audio 5s -loopback` | + feed capture into playback (feedback risk on real hardware!) |

- `-engine rtmidi|test` — MIDI driver; `rtmidi` (default) is the real ports,
  `test` uses gomidi `testdrv` loopback ports.
- `-inline` — run the TUI inline instead of the alternate screen; handy in
  terminals with altscreen quirks (e.g. Zed bottom-aligns altscreen apps).

## Containerized development (Linux, recommended for TUI work)

Day-to-day development happens in a Linux container — each run starts a
private **PipeWire session** and a virtual sound rig: playback goes to a null
sink, capture records that sink's monitor, so the audio path has a
deterministic loopback with zero host hardware:

| Command | What it does |
|---|---|
| `docker compose build` | build the `tuiper-dev` image (Go + gcc + ALSA headers + PipeWire) |
| `docker compose run --rm dev` | interactive shell in the container |
| `docker compose run --rm tuiper -engine test -demo` | TUI on loopback MIDI + synthetic events |
| `docker compose run --rm tuiper -audio 3s` | headless audio device + meter check |
| `docker compose run --rm dev go test ./internal/...` | run the tests |
| `docker compose run --rm dev go test -race ./...` | race detector over everything |

UI rendering can be verified headlessly with tmux inside the container (run
the TUI in a detached session, `send-keys`, `capture-pane -p`) — see
`NOTES.md`.

Notes: the repo is bind-mounted at `/workspace` with Go's caches in named
volumes (warm runs are quick); the container has no MIDI hardware (use the
`test` engine — which is also why `-demo` exists); on a Linux **host** with
real hardware, the `midi` profile exposes ALSA:
`docker compose --profile midi run --rm tuiper-alsa -engine rtmidi`.
Containers on Windows/macOS hosts cannot receive MIDI/audio hardware —
real-hardware testing stays on the host.

## Windows host build

- **Go** >= 1.25.6
- **mingw-w64 gcc** for CGO (RtMidi + miniaudio): `winget install
  BrechtSanders.WinLibs.POSIX.UCRT`. `build.ps1` finds it automatically.

    .\build.ps1            # vet + build tuiper.exe (CGO via mingw gcc)
    .\build.ps1 -Watch     # build, then run a headless demo smoke test
    .\build.ps1 -Zig       # build with zig cc instead (slow; cross-compile option)

The exe links the gcc runtimes statically — no mingw DLL dependencies.

## Using it as a template

- `internal/midi/` — engine/driver abstraction (RtMidi default, `testdrv`
  test engine), `Monitor` (open one input port, typed events on a channel,
  clean reopen/stop semantics, drop counter). No TUI imports.
- `internal/audio/` — engine over miniaudio (malgo): enumerate, capture,
  playback, full-duplex; per-platform backend order (WASAPI/DSound/WinMM,
  ALSA-first, CoreAudio); meter helpers. Options carry device IDs, channel
  counts; explicit-ID lifetime rules are documented in `NOTES.md`.
- `internal/ui/` — the UI shell: the device modal (state in `io_panel.go`,
  key routing + modal plumbing in `model.go`, rendering rules in `view.go`),
  activity lamps, demo sender. The panel documents the threading rules its
  own callbacks obey (audio-thread atomics, tea.Cmd opens, restart tokens).
- Rename the module (`go mod edit -module <yours>`), pick what the new app
  needs, delete the rest, and build the application on top of the `Model`.

Tests cover the UI key flow (modal open/apply/esc, backdrop freshness,
port-open regression), the monitor lifecycle against the test engine, meter
math, backend selection, and the headless modes work without a TTY.

## Project layout

    cmd/tuiper/          entrypoint, flags, headless watch/audio modes
    internal/midi/       MIDI Engine (driver owner), Monitor (event stream), test engine
    internal/audio/      audio Engine over miniaudio (devices, capture, playback, duplex, meter)
    internal/ui/         terminal UI: model.go (state/logic), io_panel.go (devices + streams),
                         view.go (render + modal composition), demo.go, open_test.go
    tools/               zig cc CGO wrappers
    scripts/             container entrypoint (PipeWire rig) + its pipewire config
    build.ps1            Windows build script
    Dockerfile,          containerized Linux dev environment
    docker-compose.yml

See `NOTES.md` for the pitfalls that were learned the hard way (frame
heights, value-copy discipline, cgo device-ID lifetimes, container audio
routing).
