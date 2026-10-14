# tuiper — template branch notes

The `template` branch is the hardened midi+audio foundation; the looper
development continues on `master`. This file collects the implementation
notes and pitfalls that the template's structure exists to satisfy.

## What is here

- `internal/midi`: engine/driver split (RtMidi real ports, gomidi `testdrv`
  test engine), `Monitor` binding one input port and delivering typed events
  on a buffered channel. Re-open closes the *already handed out* channel;
  `deliverOK` is flipped under the mutex before `close(ch)` so a delivery
  goroutine cannot send on a closed channel. Drop counter for slow
  consumers. `monitor_test.go` drives all of that headlessly.
- `internal/audio`: miniaudio (malgo) engine — enumeration, capture,
  playback, full-duplex (used by master's looper; kept because real apps
  need it). `backends()` pins the explicit per-OS backend order. `Peak`
  saturates `minInt16` instead of overflowing back to a negative value
  (which used to render a full-scale negative signal as silence).
- `internal/ui`: the shell. Device state (lists, selections, modal sections,
  streams) lives in `io_panel.go`; key routing and messages in `model.go`;
  rendering in `view.go`.

## Modal (sraaaaaaay/bubbletea-modal)

Integration rules that were found by breaking them:

- The modal composites its stored **background** over the foreground box.
  The background must be refreshed whenever the base frame changes while the
  modal is open (`Update` tail, `changedFrame(msg)`). Double value-copy
  trap: `update()` returns a fresh Model, so the refresh must mutate and
  return *that* value — refreshing the local `m.modal` and returning
  `updated` (what the first cut did) silently freezes the backdrop at open
  time.
- The refresh discards the modal's re-`Open` cmd on purpose; that cmd only
  re-emits `OpenedMsg`, and keeping it floods the update loop (Open → cmd →
  msg → Open …).
- The library sees every key first (`Update`); its confirm key is a no
  (safe-empty defaults) and esc closes. All other navigation (↑/↓, tab,
  enter-apply) is routed by the model while `modal.Opened()`.
- The frame still obeys the height rule below: the composite occupies
  exactly `m.height` rows because the stored background is the exact-height
  base frame.

## TUI gotchas (unchanged, load-bearing)

- `Model.View()` must emit **exactly `m.height` \n-separated rows with no
  trailing newline**. Bubbletea's standard renderer splits the frame on
  `\n` and, when the split count exceeds the terminal height, drops rows
  **from the top** to fit. A trailing `\n` adds one empty row, so the header
  line silently vanished in every terminal — an issue that looked like a
  Zed altscreen quirk but reproduced in tmux and Windows Terminal.
  `-inline` exists as an escape hatch for Zed's altscreen placement.
- **Value-copy discipline**: `Update` works on copies of Model. Anything
  built or mutated lazily inside a value method silently vanishes from the
  rendered model (this exact bug once killed the TUI: the monitor was
  created in a value-copy helper and `portOpenedMsg` then dereferenced nil).
  Hence: every piece of live state is created in `New` through `*`
  fields (`m.ap`, `m.monitor`, `m.lamps`) and mutated only through the
  pointer.
- Panel ↔ modal split: `modal.New(..., modal.WithForeground(ap.modalContent))`
  binds the panel *pointer* into the modal's foreground closure — that keeps
  the content live without giving the panel a back-reference to the model.

## Audio threading rules (also enforced by io_panel docs)

- Capture deliver: only atomic ops + a non-blocking channel send (drop
  counted); never block the audio thread, never allocate big buffers there.
- Streams are opened/closed in `tea.Cmd` goroutines; each restart batch gets
  a monotonic `seq` token and an out-dated open stops itself (`stream.Stop()`
  → return nil) instead of being adopted.
- Metering window (peak/frames/drops) is swapped into plain display fields
  by `Update` (`takeMeter`); `View` reads only those.

## cgo device-ID lifetime (audio.go)

`ma_device_id` is a union whose `alsa`/`pulse` members are plain strings at
offset 0 (256 bytes). Passing an ID back through `Options.DeviceID` must
land in **C memory**: a Go-managed buffer trips the cgo pointer checker, and
freeing the C copy inside `deviceConfig` (defer on its own return) leaves a
dangling pointer — ALSA then reports `Unknown PCM <garbage>`. The caller of
`malgo.InitDevice` frees the allocation.

## Container audio internals (for future maintenance)

- Each run starts a private PipeWire session (+WirePlumber, D-Bus). A null
  sink becomes the default playback target; capture uses its monitor via
  `PIPEWIRE_NODE` + `stream.capture.sink` (PipeWire 0.3.65/WirePlumber 0.4.13
  ignore a capture `target.object` pointing at a sink unless
  `stream.capture.sink = true`).
- WirePlumber without systemd-logind aborts on start (Debian bug #1019404);
  the entrypoint writes `["with-logind"] = false` into
  bluetooth.lua.d/50-bluez-config.lua. No devices appear without a session
  manager, hence the null sink in scripts/pipewire/tuiper-audio.conf.
- Verify UI rendering headlessly inside the container with tmux
  (`apt-get install tmux` on demand): run the TUI detached, `send-keys`,
  `capture-pane -p`; there are throwaway scripts under `.tmp/` for the
  flows (gitignored).
- daemon logs land in /tmp/pipewire.log and /tmp/wireplumber.log.

## Build (Windows)

Requires Go >= 1.25.6 and mingw-w64 gcc
(`winget install BrechtSanders.WinLibs.POSIX.UCRT`).

    .\build.ps1            # vet + build tuiper.exe (CGO via mingw gcc)
    .\build.ps1 -Watch     # also run the headless demo smoke test
    .\build.ps1 -Zig       # zig cc path (slow; native cross-compiler for releases)

gcc runtimes are linked statically; the exe is self-contained.
