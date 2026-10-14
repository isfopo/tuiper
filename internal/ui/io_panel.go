// Device I/O state for the template UI: everything needed to enumerate,
// select and stream MIDI input ports and audio capture/playback devices.
// The lists live here, the choices live here, and the modal (view.go) renders
// them — the main UI only mirrors the *selected* devices plus the capture
// meter.
//
// Value-copy discipline (as everywhere in this package): the panel and its
// streams are ONE pointer created in New; Update works on value copies of
// Model, so every mutation of list/selection/stream state goes through the
// *ioPanel pointer, exactly like m.monitor.
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
package ui

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tuiper/internal/audio"
)

// modal sections (tab cycles through these)
const (
	secMidi = iota
	secIn
	secOut
	secChan
	sectionCount
)

var sectionNames = [sectionCount]string{"midi in", "audio in", "audio out", "channels"}

// applied selection inside the channels section
const (
	chanStereo = 0
	chanMono   = 1
)

// channelCounts are the Options.Channels values for the channels section.
var channelCounts = []uint32{2, 1}

// ioPanel owns everything device-related for one Model instance.
type ioPanel struct {
	eng     *audio.Engine // nil when the audio backend failed to initialize
	initErr error

	// enumerations (async; the modal shows them whenever they land)
	ports []string       // midi input port names; index == port number
	ins   []audio.Device // capture devices
	outs  []audio.Device // playback devices

	section int               // active modal section
	cursors [sectionCount]int // selection cursor per section
	sel     [sectionCount]int // applied selection per section
	openNum int               // the open midi port number, -1 when closed

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

// newIoPanel initializes the audio engine eagerly (stream opens may not
// block on lazily built state) but opens nothing until the devices modal
// applies a selection.
func newIoPanel() *ioPanel {
	ap := &ioPanel{buf: make(chan []int16, 400)}
	ap.eng, ap.initErr = audio.NewEngine()
	if ap.initErr != nil {
		ap.eng = nil
	}
	return ap
}

// ---- options ----------------------------------------------------------

// inOptions returns the capture-device rows: "system default" always first,
// then every enumerated capture device.
func (ap *ioPanel) inOptions() []audio.Device {
	o := []audio.Device{{Name: "system default", IsDefault: true}}
	return append(o, ap.ins...)
}

// outOptions returns the playback-device rows.
func (ap *ioPanel) outOptions() []audio.Device {
	o := []audio.Device{{Name: "system default", IsDefault: true}}
	return append(o, ap.outs...)
}

// deviceIDFor resolves a section selection to an Options.DeviceID (nil keeps
// the system default: do not use the first enumerated device blindly).
func deviceIDFor(opts []audio.Device, sel int) []byte {
	if sel <= 0 || sel >= len(opts) {
		return nil
	}
	return opts[sel].ID()
}

func (ap *ioPanel) inName() string {
	return optionName(ap.inOptions(), ap.sel[secIn])
}

func (ap *ioPanel) outName() string {
	return optionName(ap.outOptions(), ap.sel[secOut])
}

// optionName renders the applied selection of a device list.
func optionName(opts []audio.Device, sel int) string {
	if sel <= 0 || sel >= len(opts) {
		return opts[0].Name
	}
	return opts[sel].Name
}

func (ap *ioPanel) channels() uint32 {
	return channelCounts[ap.sel[secChan]]
}

func (ap *ioPanel) chanName() string {
	return map[int]string{chanStereo: "stereo (2)", chanMono: "mono (1)"}[ap.sel[secChan]]
}

// ---- modal section navigation -----------------------------------------

// rows returns the row count of a section (0 means the modal shows a hint
// and enter does nothing).
func (ap *ioPanel) rows(sec int) int {
	switch sec {
	case secMidi:
		return len(ap.ports)
	case secIn:
		return len(ap.inOptions())
	case secOut:
		return len(ap.outOptions())
	case secChan:
		return len(channelCounts)
	}
	return 0
}

// move shifts the cursor of the active section with wrap-around.
func (ap *ioPanel) move(dir int) {
	n := ap.rows(ap.section)
	if n == 0 {
		return
	}
	ap.cursors[ap.section] = (ap.cursors[ap.section] + dir + n) % n
}

// switchSection moves to the next/previous section; the cursor jumps to that
// section's applied selection.
func (ap *ioPanel) switchSection(dir int) {
	ap.section = (ap.section + dir + sectionCount) % sectionCount
}

// selectRow commits the cursor of the active audio section to the applied
// selection and reports whether anything changed. Midi rows are applied by
// the caller (opening a port is a stream action, not a stored one).
func (ap *ioPanel) selectRow() bool {
	if ap.section == secMidi {
		return false
	}
	if ap.cursors[ap.section] == ap.sel[ap.section] {
		return false
	}
	ap.sel[ap.section] = ap.cursors[ap.section]
	return true
}

// midiPort is the port the midi section currently points at (-1 without
// ports).
func (ap *ioPanel) midiPort() int {
	if len(ap.ports) == 0 {
		return -1
	}
	return ap.cursors[secMidi]
}

// portName renders the applied midi port (what the main UI shows).
func (ap *ioPanel) portName() string {
	if ap.openNum >= 0 && ap.openNum < len(ap.ports) {
		return ap.ports[ap.openNum]
	}
	if len(ap.ports) == 0 {
		return "(none found)"
	}
	return ap.ports[ap.cursors[secMidi]] + " (not open)"
}

// appliedName renders what the given section currently has selected (what
// the main UI shows and the modal collapses other sections to).
func (ap *ioPanel) appliedName(sec int) string {
	switch sec {
	case secMidi:
		return ap.portName()
	case secIn:
		return ap.inName()
	case secOut:
		return ap.outName()
	case secChan:
		return ap.chanName()
	}
	return "?"
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
func (ap *ioPanel) listCmd() tea.Cmd {
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
// current section selections. Old streams are stopped by prepareRestart
// before this is called. One seq token covers both opens, so a newer
// restart invalidates in-flight opens at once.
func (ap *ioPanel) restartCmd() tea.Cmd {
	if ap.eng == nil {
		return nil
	}
	// snapshot all selection state on the Update thread
	seq := ap.seq.Add(1)
	opts := audio.Options{
		DeviceID: deviceIDFor(ap.inOptions(), ap.sel[secIn]),
		Channels: ap.channels(),
	}
	outID := deviceIDFor(ap.outOptions(), ap.sel[secOut])
	mon := ap.monitor

	captureCmd := func() tea.Msg {
		stream, err := ap.eng.OpenCapture(opts, ap.deliver)
		if err != nil {
			return audioErrMsg{fmt.Errorf("capture: %w", err)}
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
			return audioErrMsg{fmt.Errorf("playback: %w", err)}
		}
		if ap.seq.Load() != seq {
			stream.Stop()
			return nil
		}
		return playbackStartedMsg{stream: stream}
	}
	return tea.Batch(captureCmd, playbackCmd)
}

// ---- audio thread side ------------------------------------------------

// deliver is the capture callback: meters the block, and forwards a copy to
// the monitor buffer when the monitor path is live.
func (ap *ioPanel) deliver(in []int16) {
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
func (ap *ioPanel) pull(n int) []int16 {
	select {
	case b := <-ap.buf:
		return b
	default:
		return nil // capture not warmed up (or too slow): silence
	}
}

// shutdown stops streams and closes the engine (Model.Shutdown calls this).
func (ap *ioPanel) shutdown() {
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
func (ap *ioPanel) chip() string {
	state, color := "audio:off", "250"
	live := ap.capturing
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
		Bold(live).
		Render("[" + state + "]")
}

// ---- stream lifecycle helpers -----------------------------------------

// prepareRestart stops running streams and flushes the monitor buffer so
// restartCmd comes after a clean slate.
func (ap *ioPanel) prepareRestart() {
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

func (ap *ioPanel) drainBuf() {
	for {
		select {
		case <-ap.buf:
		default:
			return
		}
	}
}

// streaming reports whether the panel currently owns open audio streams.
func (ap *ioPanel) streaming() bool {
	return ap.capStream != nil || ap.playStream != nil
}

// takeMeter snapshots the metering window into the plain display fields that
// View reads (Update runs between renders, so this stays race-free).
func (ap *ioPanel) takeMeter() {
	ap.dispPeak = int16(ap.winPeak.Swap(0))
	ap.dispFrames = ap.winFrames.Swap(0)
	ap.dispDrops = uint64(ap.drops.Load())
}

// meterTickCmd keeps the meter row refreshing while capture runs.
func meterTickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg {
		return audioMeterMsg{}
	})
}

// ---- modal content ----------------------------------------------------

// modalContent renders the device-selection modal box (used as the modal's
// foreground; the modal library dims/composites it over the main frame).
// Sections collapse to their applied selection; the active one lists its
// rows with the cursor. All input the panel needs is in here — nothing
// reaches into the Model.
func (ap *ioPanel) modalContent() string {
	var b strings.Builder

	title := " devices "
	write := func(s string) { b.WriteString(s + "\n") }

	secLine := func(sec int) string {
		name := sectionNames[sec]
		applied := ap.appliedName(sec)
		if sec == ap.section {
			return lipgloss.NewStyle().
				Foreground(lipgloss.Color("205")).Bold(true).
				Render("> "+name+"") +
				"  " + selectedStyle.Render(applied)
		}
		return "  " + name + "  " + applied
	}

	b.WriteString(title + "\n")
	b.WriteString("\n")
	for sec := 0; sec < sectionCount; sec++ {
		write(secLine(sec))
		if sec != ap.section {
			continue
		}
		rows := ap.rows(sec)
		if rows == 0 {
			write("      " + errStyle.Render("(nothing found — press r to refresh)"))
			continue
		}
		for i := 0; i < rows; i++ {
			if i == ap.cursors[sec] {
				write("      > " + selectedStyle.Render(ap.rowText(sec, i)))
				continue
			}
			write("        " + ap.rowText(sec, i))
		}
	}
	b.WriteString("\n")
	write(helpStyle.Render("  ↑/↓ move • tab section • enter apply • esc close"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 2).
		Render(b.String())
}

// rowText renders one modal row of the given section.
func (ap *ioPanel) rowText(sec, i int) string {
	switch sec {
	case secMidi:
		return ap.ports[i]
	case secIn:
		return ap.inOptions()[i].String()
	case secOut:
		return ap.outOptions()[i].String()
	case secChan:
		return fmt.Sprintf("%d channels", channelCounts[i])
	}
	return "?"
}
