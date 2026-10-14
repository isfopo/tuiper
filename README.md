# tuiper

A looper with a terminal UI (Bubble Tea), intended to run against a DAW
(Ableton) via Ableton Link and MIDI. Currently past **Milestone 1: MIDI
monitor** (`-engine test -demo` TUI, `-watch`) and exercising **Milestone 2:
audio I/O** (`-audio` device check).

## Containerized development (Linux, recommended for TUI work)

Day-to-day development of the TUI happens in a Linux container — Go lives
where it is happiest, and no per-machine toolchain setup is needed. Each
container run starts a private **PipeWire session** (audio + MIDI) and a
virtual sound rig: playback goes to a null sink and capture records that
sink's monitor, so the audio path has a deterministic loopback with zero host
hardware:

| Command | What it does |
|---|---|
| `docker compose build` | build the `tuiper-dev` image (Go + gcc + ALSA headers + PipeWire) |
| `docker compose run --rm dev` | interactive shell in the container |
| `docker compose run --rm tuiper -engine test -demo` | TUI on loopback MIDI ports + synthetic events |
| `docker compose run --rm tuiper -engine test -demo -watch 3s` | headless MIDI smoke test |
| `docker compose run --rm tuiper -audio 3s` | audio devices + capture meter (silent) |
| `docker compose run --rm tuiper -audio 3s -loopback` | capture→playback round trip |
| `docker compose run --rm dev go test ./internal/...` | run the tests |
| `docker compose run --rm dev go vet ./...` | vet |

Notes:

- The repo is bind-mounted at `/workspace`; Go's build and module caches
  live in named volumes (`tuiper_go-build`, `tuiper_go-mod`), so a warm
  `docker compose run` is only a few seconds.
- The container has no MIDI hardware: use the `test` engine there (this is
  also why `-demo` exists). `rtmidi` compiles fine and simply lists zero
  ports.
- The PipeWire bootstrap (`scripts/container-entrypoint.sh`) starts
  pipewire, wireplumber and a private D-Bus session, and routes every
  alsa-pipewire plugin client to the virtual rig (`scripts/pipewire/`);
  backend logs land in `/tmp/pipewire.log` inside the container.
- On a **Linux host** with real MIDI/audio hardware, the `midi` profile
  exposes ALSA and leaves capture routing alone:
  `docker compose --profile midi run --rm tuiper-alsa -engine rtmidi`.
- Containers on Windows/macOS hosts cannot receive MIDI/audio hardware:
  WinMM/CoreMIDI/WASAPI endpoints are not exposed by Docker Desktop.
  Real-hardware testing stays on the host (Windows build below).

## Requirements (Windows host build)

- **Go** >= 1.24
- **mingw-w64 gcc** for CGO (RtMidi), installed via
  `winget install BrechtSanders.WinLibs.POSIX.UCRT`. `build.ps1` finds it
  automatically (on `PATH` or in the WinGet package directory).
- Optionally **zig** (`winget install zig.zig`) for cross-compiling later;
  use `.\build.ps1 -Zig` to build with it. Note: the zig path is currently
  slow on Windows — each rebuild re-runs the full cgo invocation chain and
  Go's build cache is not reused, so expect ~1–2 minutes per build.

## Build (Windows)

.\build.ps1            # vet + build tuiper.exe (CGO via mingw gcc)
.\build.ps1 -Watch     # build, then run a headless demo smoke test
.\build.ps1 -Zig       # build with zig cc instead (slow, see above)

This outputs `tuiper.exe` in the repo root. The build script enables CGO,
points `CC`/`CXX` at the mingw gcc compiler, and links the gcc runtimes
statically (`CGO_LDFLAGS=-static-libstdc++ -static-libgcc -static`) so the
exe has no mingw DLL dependencies.

For TUI iteration prefer the containerized workflow; the Windows build is
for real-hardware testing and releases.

## Run

| Command | What it does |
|---|---|
| `.\tuiper.exe` | TUI on real MIDI ports (RtMidi via CGO: WinMM/CoreMIDI/ALSA) |
| `.\tuiper.exe -inline` | TUI without the alternate screen (for terminals that hide the top line, e.g. Zed) |
| `.\tuiper.exe -engine test` | TUI on fake loopback ports (no hardware or CGO needed) |
| `.\tuiper.exe -engine test -demo` | Fake ports plus a synthetic event sender |
| `.\tuiper.exe -watch 5s` | Headless: print events for 5 seconds (smoke test/debugging) |
| `.\tuiper.exe -audio 5s` | Headless audio: enumerate WASAPI devices, meter default capture |
| `.\tuiper.exe -audio 5s -loopback` | + feed capture into playback (feedback risk: mic into speakers!) |

Flags:

- `-engine rtmidi|test` — MIDI driver. `rtmidi` is the default (real ports);
  `test` uses gomidi `testdrv` loopback ports.
- `-demo` — inject synthetic events (test engine only).
- `-watch <duration>` — headless event printing instead of the TUI.
- `-audio <duration>` — headless audio check (milestone 2): lists devices,
  meters the default capture (peak in dBFS). `-loopback` additionally plays
  captured audio straight out of the default output — opt-in because on real
  hardware that is a live monitor/feedback loop.

- `-inline` — run the TUI inline instead of in the alternate screen; use in
  terminals with altscreen quirks (Zed's integrated terminal has hidden the
  first line of altscreen apps).

## TUI keys

- `q` / `ctrl+c` — quit (`esc` first cancels an open audio dropdown)
- `↑`/`k`, `↓`/`j` — move selection (inside an open dropdown: its entries)
- `enter` / `space` — open/close the selected MIDI port, or confirm the open
  audio dropdown
- `i` / `o` / `c` — audio dropdowns: capture device, playback device,
  channel count (stereo/mono). Selecting re-opens the streams.
- `m` — toggle the monitor path (capture → playback). In the container the
  rig makes this safe; on real hardware mic→speakers is a live feedback loop.
- `r` — refresh lists (ports + audio devices)

Audio captures start only after you pick a device; the `in` row shows a live
peak meter (dBFS + frames/s) once capture runs.

Incoming events are not scrolled as a log anymore (the `-watch` headless
mode still prints events); in the TUI traffic shows as the counters in the
status line (`events=… clocks=…`) plus the header activity dots.

## Project layout

    cmd/tuiper/          entrypoint, flags, headless watch/audio modes
    internal/midi/       MIDI Engine (driver owner), Monitor (event stream)
    internal/audio/      audio Engine over miniaudio (capture/playback/Devices)
    internal/ui/         terminal UI: model.go (state/logic), view.go (render),
                         demo.go, audio_panel.go (device dropdowns/meter/monitor)
    tools/               zig cc CGO wrappers
    scripts/             container entrypoint (PipeWire rig) + its pipewire config
    build.ps1            Windows build script
    Dockerfile,          containerized Linux dev environment (see README)
    docker-compose.yml

## Roadmap

1. ✅ MIDI monitor (this milestone)
2. ✅ Audio I/O headless core: enumeration, capture meter, capture→playback round trip
3. 🚧 Device/channel selection in the TUI (audio dropdowns + meter, monitor path)
4. Single-channel looper: record/overdub/play/stop/clear, tempo-synced edges
5. MIDI pedal mapping (config file: device, note/CC -> action)
6. Waveform display + position cursor on the TUI
7. Multichannel/track stack, loop-in-loop
8. Ableton Link clock sync (UDP protocol, Go binding or port)
9. Release packaging (prebuilt binaries, `-engine rtmidi` embedding)

See `NOTES.md` for build/packaging notes.
