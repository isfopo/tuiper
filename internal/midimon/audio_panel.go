// Audio device selection for the TUI (milestone 2): dropdowns for capture
// and playback devices, a channel-count choice, a live capture meter, and an
// optional monitor path (capture → playback) to prove the round trip.
//
// Value-copy discipline (same lesson as the MIDI monitor in model.go): the
// panel and all its streams are pointer fields created in New; Update works
// on value copies of Model, so anything assigned while handling a copy would
// silently vanish. Every mutation of stream state happens through the *audio
// Panel pointer, exactly like m.monitor.
//
// Audio threading rules respected here:
//
//   - deliver runs on the audio thread: only atomic writes and a non-binding
//     channel send (drops counted, never block).
//   - stream open/close happens in tea.Cmd goroutines (restart sequences get
//     a monotonic token; a late-arriving open for an outdated configuration
//     stops itself instead of being adopted).
//   - meter reads happen on Update; View only reads the plain display fields
//     filled by Update between renders.
package midimon

import (
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tuiper/internal/audio"
)

// dropdown subjects
const (
	dropNone = iota
	dropIn
	dropOut
	dropChan
)

// channel entries (indices into the channels dropdown)
const (
	chanStereo = 0
	chanMono   = 1
)

// channelCounts are the Options.Channels values for the channels dropdown.
var channelCounts = []uint32{2, 1}

// audioPanel owns everything audio for one Model instance.
type audioPanel struct {
	eng     *audio.Engine // nil when the audio backend failed to initialize
	initErr error

	ins, outs []audio.Device // last enumeration (may be nil until listed)

	focus  int // open dropdown: dropNone/dropIn/dropOut/dropChan
	cursor int // cursor inside the open dropdown

	inSel  int // index into inOptions(): 0 = system default, 1.. = ins[i-1]
	outSel int // index into outOptions()
	chans  int // index into channelCounts

	seq atomic.Int64 // restart token: only the newest async open is adopted

	capStream *audio.Stream
	capturing bool

	// meter (windows read+reset in Update; only atomics touched on the
	// audio thread)
	winPeak    atomic.Int64 // max magnitude since last read
	winFrames  atomic.Int64 // frames since last read
	dispPeak   int16        // snapshot for View
	dispFrames int64        // snapshot for View
	dispDrops  uint64       // snapshots for View

	// monitor path: captured blocks are pushed to the selected output
	monitor    bool
	monOn      atomic.Bool // gates deliver; true only while playStream runs
	buf        chan []int16
	drops      atomic.Int64
	playStream *audio.Stream
}

// newAudioPanel initializes the engine eagerly (like the MIDI monitor is
// created in New, never in a toggle) and starts with the system default
// devices selected but nothing opened until the user picks a device.
func newAudioPanel() *audioPanel {
	ap := &audioPanel{buf: make(chan []int16, 400)}
	ap.eng, ap.initErr = audio.NewEngine()
	if ap.initErr != nil {
		ap.eng = nil
	}
	return ap
}

// ---- options ----------------------------------------------------------

// inOptions returns the capture-device dropdown: "system default" always
// first, then every enumerated capture device.
func (ap *audioPanel) inOptions() []audio.Device {
	o := []audio.Device{{Name: "system default", IsDefault: true}}
	return append(o, ap.ins...)
}

// outOptions returns the playback-device dropdown entries.
func (ap *audioPanel) outOptions() []audio.Device {
	o := []audio.Device{{Name: "system default", IsDefault: true}}
	return append(o, ap.outs...)
}

// deviceID resolves a dropdown selection to an Options.DeviceID (nil keeps
// the system default: do not use the first enumerated device blindly).
func deviceIDFor(opts []audio.Device, sel int) []byte {
	if sel <= 0 || sel >= len(opts) {
		return nil
	}
	return opts[sel].ID()
}

func (ap *audioPanel) inName() string {
	if ap.inSel == 0 {
		return "system default"
	}
	if ap.inSel <= len(ap.ins) {
		return ap.ins[ap.inSel-1].Name
	}
	return "?"
}

func (ap *audioPanel) outName() string {
	if ap.outSel == 0 {
		return "system default"
	}
	if ap.outSel <= len(ap.outs) {
		return ap.outs[ap.outSel-1].Name
	}
	return "?"
}

func (ap *audioPanel) channels() uint32 {
	return channelCounts[ap.chans]
}

// ---- messages ---------------------------------------------------------

type audioListMsg struct {
	ins  []audio.Device
	outs []audio.Device
}

type audioErrMsg struct{ err error }

type captureStartedMsg struct {
	stream *audio.Stream
}

type playbackStartedMsg struct {
	stream *audio.Stream
}

type audioMeterMsg struct{}

// ---- commands ---------------------------------------------------------

// listCmd enumerates capture/playback devices (async: WASAPI/ALSA
// enumeration can block for a noticeable while).
func (ap *audioPanel) listCmd() tea.Cmd {
	return func() tea.Msg {
		if ap.eng == nil {
			return audioErrMsg{ap.initErr}
		}
		ins, err := ap.eng.Inputs()
		if err != nil {
			return audioErrMsg{err}
		}
		outs, err := ap.eng.Outputs()
		if err != nil {
			return audioErrMsg{err}
		}
		return audioListMsg{ins: ins, outs: outs}
	}
}

// restartCmd re-opens capture (and playback when the monitor is on) with the
// current dropdown state. Old streams are stopped by prepareRestart before
// this is called. One seq token covers both opens so a newer restart
// invalidates both in-flight opens at once.
func (ap *audioPanel) restartCmd() tea.Cmd {
	if ap.eng == nil {
		return nil
	}
	// snapshot all selection state on the Update thread
	seq := ap.seq.Add(1)
	opts := audio.Options{
		DeviceID: deviceIDFor(ap.inOptions(), ap.inSel),
		Channels: ap.channels(),
	}
	outID := deviceIDFor(ap.outOptions(), ap.outSel)
	mon := ap.monitor

	captureCmd := func() tea.Msg {
		stream, err := ap.eng.OpenCapture(opts, ap.deliver)
		if err != nil {
			return audioErrMsg{err}
		}
		if ap.seq.Load() != seq { // a newer restart won: abort
			stream.Stop()
			return nil
		}
		return captureStartedMsg{stream: stream}
	}

	if !mon {
		return captureCmd
	}

	playbackCmd := func() tea.Msg {
		stream, err := ap.eng.OpenPlayback(audio.Options{
			DeviceID: outID,
			Channels: opts.Channels,
		}, ap.pull)
		if err != nil {
			return audioErrMsg{err}
		}
		if ap.seq.Load() != seq {
			stream.Stop()
			return nil
		}
		return playbackStartedMsg{stream: stream}
	}
	return tea.Batch(captureCmd, playbackCmd)
}

// ---- audio thread side -------------------------------------------------

// deliver is the capture callback: meters the block, and forwards a copy to
// the monitor buffer when the monitor path is live.
func (ap *audioPanel) deliver(in []int16) {
	n := len(in) / 2
	ap.winFrames.Add(int64(n))
	peak := audio.Peak(in)
	for {
		old := ap.winPeak.Load()
		if int64(peak) <= old || ap.winPeak.CompareAndSwap(old, int64(peak)) {
			break
		}
	}

	if !ap.monOn.Load() {
		return
	}
	block := make([]int16, len(in)) // fresh copy: capture buffer is reused
	copy(block, in)
	select {
	case ap.buf <- block:
	default:
		ap.drops.Add(1)
	}
}

// pull is the playback callback (drains the monitor buffer).
func (ap *audioPanel) pull(n int) []int16 {
	select {
	case b := <-ap.buf:
		return b
	default:
		return nil // capture not warmed up (or too slow): silence
	}
}

// shutdown stops streams and closes the engine (Model.Shutdown calls this).
func (ap *audioPanel) shutdown() {
	if ap.capStream != nil {
		ap.capStream.Stop()
		ap.capStream = nil
	}
	if ap.playStream != nil {
		ap.playStream.Stop()
		ap.playStream = nil
	}
	ap.monOn.Store(false)
	if ap.eng != nil {
		ap.eng.Close()
	}
}

// chip renders the compact audio state for the status bar.
func (ap *audioPanel) chip() string {
	state, color := "audio:off", "250"
	switch {
	case ap.monitor && ap.playStream != nil:
		state, color = "audio:monitor", "213"
	case ap.capturing:
		state, color = "audio:live", "42"
	case ap.eng == nil:
		state = "audio:err"
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(color)).
		Bold(ap.capturing).
		Render("[" + state + "]")
}

// ---- dropdown state helpers -------------------------------------------

// prepareRestart stops running streams and flushes the monitor buffer so
// restartCmd comes after a clean slate.
func (ap *audioPanel) prepareRestart() {
	if ap.capStream != nil {
		ap.capStream.Stop()
		ap.capStream = nil
	}
	if ap.playStream != nil {
		ap.playStream.Stop()
		ap.playStream = nil
	}
	ap.monOn.Store(false)
	ap.drainBuf()
}

func (ap *audioPanel) drainBuf() {
	for {
		select {
		case <-ap.buf:
		default:
			return
		}
	}
}

// toggleDropdown opens the focused dropdown (or closes it if already open).
func (ap *audioPanel) toggleDropdown(which int) {
	if ap.focus == which {
		ap.focus = dropNone
		return
	}
	ap.focus = which
	ap.cursor = ap.currentSelection(which)
}

// currentSelection maps an open dropdown subject to its selection index.
func (ap *audioPanel) currentSelection(focus int) int {
	switch focus {
	case dropIn:
		return ap.inSel
	case dropOut:
		return ap.outSel
	case dropChan:
		return ap.chans
	}
	return 0
}

// dropdownCount is the number of entries of the currently open dropdown.
func (ap *audioPanel) dropdownCount() int {
	switch ap.focus {
	case dropIn:
		return len(ap.inOptions())
	case dropOut:
		return len(ap.outOptions())
	case dropChan:
		return len(channelCounts)
	default:
		return 0
	}
}

// moveCursor moves the cursor inside the open dropdown with wrap-around.
func (ap *audioPanel) moveCursor(delta int) {
	n := ap.dropdownCount()
	if n == 0 {
		return
	}
	ap.cursor = (ap.cursor + delta + n) % n
}

// pick selects the entry under the cursor; the caller restarts the streams.
// Returns true when the selection actually moved (restart only then).
func (ap *audioPanel) pick() bool {
	if ap.focus == dropNone {
		return false
	}
	sel := ap.cursor
	changed := sel != ap.currentSelection(ap.focus)
	switch ap.focus {
	case dropIn:
		ap.inSel = sel
	case dropOut:
		ap.outSel = sel
	case dropChan:
		ap.chans = sel
	}
	ap.focus = dropNone
	return changed
}

// takeMeter snapshots the metering window into the plain display fields that
// View reads (Update runs between renders, so this stays race-free).
func (ap *audioPanel) takeMeter() {
	ap.dispPeak = int16(ap.winPeak.Swap(0))
	ap.dispFrames = ap.winFrames.Swap(0)
	ap.dispDrops = uint64(ap.drops.Load())
}

// meterTickCmd keeps the meter subplot refreshing while capture runs.
func meterTickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg {
		return audioMeterMsg{}
	})
}
