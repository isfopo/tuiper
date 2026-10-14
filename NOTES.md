# tuiper

A looper with a terminal UI (Bubble Tea), intended to run against a DAW
(Ableton) via Ableton Link and MIDI.

## Current status — Milestone 2: audio I/O (in progress), milestone 1 done

- Bubble Tea MIDI monitor: port list, open/close, live event log.
- Engines:
  - `rtmidi` (default): real ports via RtMidi/CGO (WinMM/CoreMIDI/ALSA).
  - `test`: fake loopback ports via gomidi `testdrv`, no CGO in the path.
- `-demo`: synthetic event sender on the test engine.
- `-watch 5s`: headless event printing (smoke test / debugging).
- `-audio 5s`: headless audio check over miniaudio (via malgo CGO):
  enumerates devices, meters default capture (peak dBFS). Space backend
  order: WASAPI (Win), ALSA-first (Linux). `-loopback` feeds capture into
  playback for round-trip tests (in containers the pipewire null sink +
  monitor routing make this a safe, deterministic loop).
- Container audio: each run starts a private PipeWire session (+WirePlumber,
  D-Bus); a null sink becomes the default playback target and capture uses
  its monitor via the `PIPEWIRE_NODE`/`stream.capture.sink` route below.
  WirePlumber 0.4 needs the Debian workaround and the container needs the
  virtual rig config (see scripts/).

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

## TUI gotchas

- `Model.View()` must emit **exactly `m.height` \n-separated rows with no
  trailing newline**. Bubbletea's standard renderer splits the frame on `\n`
  and, when the split count exceeds the terminal height, drops rows **from
  the top** to fit. A trailing `\n` adds one empty row, so the header line
  (title + activity lamps) was silently erased in every terminal — it looked
  like a Zed altscreen quirk but reproduced in tmux and Windows Terminal too.
- Reproduce/verify UI rendering headlessly inside the dev container with
  tmux (`apt-get install tmux` on demand): run the TUI in a detached
  session, `send-keys` keys, then `capture-pane -p` (plain text) and
  `pipe-pane` the raw output stream to a log for byte-level analysis.

## Packaging notes (kept in mind for later)

- `tools/zigcc.cmd` / `tools/zigcxx.cmd` are the only nonstandard build
  machinery for the zig path; a release build can run `go build` with CGO=1
  and `-Zig` (or gcc equivalents per host). zig is also a native cross
  compiler, so Windows/macOS/Linux release targets can come from one machine
  eventually.
- The `test` engine proves a workflow with no hardware/toolchain lock-in.

## Container audio internals (gotchas, for future maintenance)

- **WirePlumber without systemd-logind**: Debian's build aborts on start
  (Debian bug #1019404); the entrypoint writes
  `["with-logind"] = false` into bluetooth.lua.d/50-bluez-config.lua.
- **No devices appear without a session manager**: PipeWire alone ships no
  default sink; WirePlumber only creates nodes for real ALSA cards (none in
  a container), hence the null sink in scripts/pipewire/tuiper-audio.conf.
- **Capture-routing (PipeWire 0.3.65 + WirePlumber 0.4.13)**: a capture
  stream's explicit `target.object`/`node.target` pointing at a *sink* is
  ignored unless the stream also has `stream.capture.sink = true`
  (policy-node.lua routes it to the default source otherwise). The
  alsa-pipewire plugin accepts pipe-wide props via `PIPEWIRE_ALSA` env
  (arbitrary property update string) plus `PIPEWIRE_NODE` (target override);
  the entrypoint exports both so every ALSA plugin client (arecord, aplay,
  miniaudio) captures the null sink's monitor — a deterministic loopback.
- **miniaudio explicit device IDs (audio.go)**: `ma_device_id` is a union
  whose `alsa`/`pulse` members are plain strings at offset 0 (256 bytes).
  Passing the ID back through `Options.DeviceID` must land in **C memory**:
  a Go-managed buffer handed into `ma_device_init` trips the cgo pointer
  checker ("Go pointer to unpinned buffer"), and letting `deviceConfig`
  free its C copy too early (defer on its own return) leaves a dangling
  pointer — ALSA then reports `Unknown PCM <garbage>` / MA_DOES_NOT_EXIST.
  The ID allocation is therefore freed by the caller of
  `malgo.InitDevice`.
- daemon logs land in /tmp/pipewire.log and /tmp/wireplumber.log inside
  containers.

## Roadmap (rough order)

1. x  MIDI monitor (this milestone)
2. x  Audio I/O: enumeration + capture meter + capture→playback round trip
   done headless (-audio/-loopback); TUI device dropdowns + meter + monitor
   path wired into the monitor view ('i'/'o'/'c'/'m').
3. Single-channel looper: record/overdub/play/stop/clear, tempo-synced edges
4. MIDI pedal mapping (config file: device, note/CC -> action)
5. Waveform display + position cursor on the TUI
6. Multichannel/track stack, loop-in-loop
7. Ableton Link clock sync (UDP protocol, Go binding or port)
8. Release packaging (prebuilt binaries, `-engine rtmidi` embedding)

## Layout

    cmd/tuiper/          entrypoint, flags, headless watch/audio modes
    internal/midi/       MIDI Engine (driver owner), Monitor (event stream)
    internal/audio/      audio Engine (miniaudio via malgo: Devices, capture, playback)
    internal/ui/         terminal UI: model.go (state/logic), view.go (render),
                         demo.go, audio_panel.go (device dropdowns/meter/monitor)
    tools/               zig cc CGO wrappers
    scripts/             container entrypoint + pipewire virtual rig config
