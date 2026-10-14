// Package ui implements the template terminal UI: a Bubble Tea based monitor
// that lists MIDI input ports and audio devices in a single modal (open/
// close with 'd'), mirrors the applied selections in the main UI, and shows
// a live capture meter. Application logic (loopers, sequencers, ...) goes on
// top of this file's foundations.
//
// Structure notes (Elm architecture, as used by Bubble Tea):
//
//	Model   - all app state, plain struct
//	Init()  - commands to run at startup
//	Update() - (model, incoming message) -> (model, followup commands)
//	View()  - render current state to a string (the devices modal overlays
//	          the exact same frame; the base view occupies exactly one
//	          terminal row per rendered line, see view.go)
//
// Driver details live in internal/midi and internal/audio; this package only
// knows about ports/devices, events, and its own state.
package ui

import (
	"fmt"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sraaaaaaay/bubbletea-modal"

	gomidi "gitlab.com/gomidi/midi/v2"

	"tuiper/internal/midi"
)

// lamps are the header's MIDI activity indicators (dim by default, lit
// briefly by traffic). Stamps are atomics so the demo sender goroutine can
// record outgoing sends while Update/View read them.
type lamps struct {
	in       atomic.Int64 // last received midi event
	out      atomic.Int64 // last sent midi event
	blinking atomic.Bool  // a lamp-off repaint is already scheduled
}

// lit reports whether a stamp is inside the blink window.
func (l *lamps) lit(stamp *atomic.Int64) bool {
	t := stamp.Load()
	return t > 0 && time.Now().UnixNano()-t < int64(blinkWindow)
}

// lampOffCmd schedules the repaint that turns a lit lamp back off. The
// blinking flag debounces: while events keep arriving, one pending repaint is
// enough; after traffic stops, one extra tick douses the lamp again.
func lampOffCmd() tea.Cmd {
	return tea.Tick(blinkWindow, func(time.Time) tea.Msg {
		return lampOffMsg{}
	})
}

// lampOffMsg repaints the header with expired lamps.
type lampOffMsg struct{}

// newLamps anchors the lamp state so it survives Model value copies.
func newLamps() *lamps { return &lamps{} }

// blinkWindow is how long a lamp stays lit after observed MIDI traffic.
const blinkWindow = 250 * time.Millisecond

// Model holds all state of the monitor.
type Model struct {
	width, height int

	eng     *midi.Engine // owns the midi driver
	monitor *midi.Monitor

	demo    bool // -demo mode: synthetic events on the test engine
	demoCtl *DemoSender

	ap *ioPanel // device lists/selections/streams (always non-nil after New)

	// devices modal: covers the main frame while open (marker.Model is a
	// value like Model itself; assignments only happen inside Update paths
	// so they reach the rendered model)
	modal modal.Model

	// MIDI activity lamps: receive/send stamps (unix nanos) written via
	// atomics from whichever goroutine observes traffic; View decides bright
	// vs. dark from the elapsed time since the stamp.
	lamps *lamps

	events  uint64 // total received
	clocks  uint64 // timing clock / tick, counted separately (often spammy)
	dropped uint64 // dropped by the monitor buffer
	status  string // last status line
	isErr   bool
}

// New returns a fresh model. eng chooses the MIDI driver to work with; demo
// enables the synthetic event sender (test engine only).
//
// The monitor is created here, never in an Update-time helper: Update works
// on value copies of Model, so something built lazily from a copy would never
// reach the model the TUI keeps rendering.
func New(eng *midi.Engine, demo bool) Model {
	ap := newIoPanel()
	m := Model{
		eng:     eng,
		monitor: midi.NewMonitor(eng),
		demo:    demo,
		ap:      ap,
		lamps:   newLamps(),
		status:  "d opens the device modal",
		height:  24,
		width:   80,
	}
	m.ap.openNum = -1
	m.modal = modal.New(
		modal.WithForeground(ap.modalContent),
		modal.WithDimmedBackground(true),
		modal.WithKeyMap("enter", "esc"),
	)
	if demo {
		// created here (not in togglePort): togglePort works on value
		// copies, so the sender would never reach the rendered model
		m.demoCtl = NewDemoSender(eng)
		m.demoCtl.OnSend(func() { m.lamps.out.Store(time.Now().UnixNano()) })
	}
	return m
}

// Shutdown stops the demo sender (if any) and the listener, and returns the
// engine for final close by main. Called after Program.Run returns (the
// returned final model is us).
func (m Model) Shutdown() *midi.Engine {
	if m.demoCtl != nil {
		<-m.demoCtl.Stop()
		m.demoCtl = nil
	}
	if m.monitor != nil {
		m.monitor.Stop()
	}
	if m.ap != nil {
		m.ap.shutdown()
	}
	return m.eng
}

// ---- messages ---------------------------------------------------------

type portsMsg struct {
	ports []string
	err   error
}

type portOpenedMsg struct{ num int }

type portOpenErrMsg struct {
	num int
	err error
}

type portClosedMsg struct{ num int }

type eventMsg struct{ Event midi.Event }

// eventsStoppedMsg signals that the monitor event channel closed
// (stop or reopen).
type eventsStoppedMsg struct{}

// ---- commands ---------------------------------------------------------

// portsCmd enumerates midi input ports (async: driver enumeration can block).
func (m Model) portsCmd() tea.Cmd {
	return func() tea.Msg {
		ports, err := m.eng.InputPorts()
		return portsMsg{ports: ports, err: err}
	}
}

// waitForEventCmd keeps the event subscription alive: Update re-issues it
// after every eventMsg received, so exactly one receive sits on the channel
// between renders. When the channel closes (reopen/stop) it reports that.
func waitForEventCmd(ch <-chan midi.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsStoppedMsg{}
		}
		return eventMsg{Event: ev}
	}
}

// ---- Init / Update ----------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.portsCmd(), m.ap.listCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.update(msg)
	// keep the modal backdrop current: re-capture the base frame whenever
	// state changed underneath it. NOTE: update() returned a fresh model
	// value — refresh THAT one and return it, or the refresh silently
	// vanishes (value-copy discipline cut me here twice).
	if mm, ok := updated.(Model); ok && mm.modal.Opened() && changedFrame(msg) {
		mm.modal, _ = mm.modal.Open(mm.baseView())
		return mm, cmd
	}
	return updated, cmd
}

// changedFrame marks messages that can alter the base frame (backdrop).
func changedFrame(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.WindowSizeMsg, portsMsg, audioListMsg, captureStartedMsg,
		playbackStartedMsg, audioMeterMsg, eventMsg, lampOffMsg,
		portClosedMsg:
		return true
	}
	return false
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.modal.Opened() {
			return m.updateModalKeys(msg)
		}
		switch msg.String() {

		case "q", "ctrl+c":
			return m, tea.Quit

		case "esc", "ctrl+d":
			return m, tea.Quit

		case "d", "?":
			// open the devices modal over the current frame; refresh the
			// device lists in the background so it fills in as it arrives
			var open tea.Cmd
			m.modal, open = m.modal.Open(m.baseView())
			return m, tea.Batch(open, m.portsCmd(), m.ap.listCmd())

		case "m":
			return m.toggleMonitor()

		case "r":
			return m, tea.Batch(m.portsCmd(), m.ap.listCmd())
		}

	case portsMsg:
		if msg.err != nil {
			m.fail("list ports: %v", msg.err)
			return m, nil
		}
		m.ap.ports = msg.ports
		if c := m.ap.cursors[secMidi]; c >= len(msg.ports) {
			m.ap.cursors[secMidi] = 0
		}
		return m, nil

	case portOpenedMsg:
		m.ap.openNum = msg.num
		m.ok("listening: %s", m.ap.ports[msg.num])
		cmds := []tea.Cmd{waitForEventCmd(m.monitor.Events())}
		if m.demo && m.demoCtl != nil {
			m.demoCtl.Start()
		}
		return m, tea.Batch(cmds...)

	case portOpenErrMsg:
		m.fail("open port %d: %v", msg.num, msg.err)
		return m, nil

	case portClosedMsg:
		m.ap.openNum = -1
		if m.demoCtl != nil {
			<-m.demoCtl.Stop()
			m.demoCtl = nil
		}
		m.ok("closed port %d", msg.num)
		return m, nil

	case eventsStoppedMsg:
		// listener stopped (reopen or close); nothing to re-subscribe until
		// a port is opened again
		return m, nil

	case eventMsg:
		return m.updateEvent(msg.Event)

	case lampOffMsg:
		if m.lamps != nil {
			m.lamps.blinking.Store(false)
		}
		return m, nil // repaint: any lamp whose blink window expired goes dark

	// ---- audio panel -----------------------------------------------------

	case audioListMsg:
		m.ap.ins = msg.ins
		m.ap.outs = msg.outs
		// clamp cursors/selections against the new lists (2 = default row
		// always exists for in/out, so only idx > len is invalid)
		for sec, rows := range map[int]int{secIn: len(m.ap.inOptions()), secOut: len(m.ap.outOptions())} {
			if m.ap.cursors[sec] >= rows && rows > 0 {
				m.ap.cursors[sec] = 0
			}
		}
		if len(m.ap.ins) > 0 && m.ap.sel[secIn] > len(m.ap.ins) {
			m.ap.sel[secIn] = 0
		}
		if len(m.ap.outs) > 0 && m.ap.sel[secOut] > len(m.ap.outs) {
			m.ap.sel[secOut] = 0
		}
		m.ok("devices: %d midi / %d capture / %d playback", len(m.ap.ports), len(msg.ins), len(msg.outs))
		return m, nil

	case audioErrMsg:
		m.fail("audio: %v", msg.err)
		return m, nil

	case captureStartedMsg:
		m.ap.capStream = msg.stream
		m.ap.capturing = true
		// the monitor gate opens fully only once playback is up too
		m.ap.monOn.Store(m.ap.monitor && m.ap.playStream != nil)
		m.ok("audio capture: %s (%d ch)", m.ap.inName(), m.ap.channels())
		return m, meterTickCmd()

	case playbackStartedMsg:
		m.ap.playStream = msg.stream
		m.ap.monOn.Store(m.ap.monitor)
		m.ok("audio monitor: %s → %s", m.ap.inName(), m.ap.outName())
		return m, nil

	case audioMeterMsg:
		m.ap.takeMeter()
		if m.ap.capturing {
			return m, meterTickCmd()
		}
		return m, nil
	}

	return m, nil
}

// updateModalKeys routes keys that arrive while the devices modal is open:
// the modal library sees every key (its esc/confirm semantics stay intact),
// while navigation/apply are handled by the panel and the model.
func (m Model) updateModalKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var mod tea.Model
	mod, cmd = m.modal.Update(msg)
	m.modal = mod.(modal.Model)

	switch msg.String() {
	case "esc":
		// the modal library closed itself; drop back to the main frame
		m.ok("devices closed")
		return m, cmd

	case "up", "k":
		m.ap.move(-1)
		return m, nil

	case "down", "j":
		m.ap.move(1)
		return m, nil

	case "tab":
		m.ap.switchSection(1)
		return m, nil

	case "shift+tab":
		m.ap.switchSection(-1)
		return m, nil

	case "r":
		return m, tea.Batch(cmd, m.portsCmd(), m.ap.listCmd())

	case "enter":
		// the modal's confirm cmd is a no-op; the apply semantics are ours
		return m, tea.Batch(cmd, m.applySelection())
	}

	return m, cmd
}

// applySelection applies the cursor row of the active modal section:
//
//	midi in — opens/closes the pointed-at port (stream action)
//	audio rows — update the stored selection and re-open the streams
func (m Model) applySelection() tea.Cmd {
	switch m.ap.section {
	case secMidi:
		num := m.ap.midiPort()
		if num < 0 {
			m.fail("no midi input ports")
			return nil
		}
		if m.ap.openNum == num {
			return closePortCmd(m.monitor, num)
		}
		return openPortCmd(m.monitor, num)

	case secIn:
		// applying a capture device (re)opens capture right away, even when
		// the row is the already-applied selection: enter means "open this"
		// (meter turns on the main row)
		m.ap.selectRow()
		m.ap.prepareRestart()
		m.ok("audio in: %s", m.ap.inName())
		return m.ap.restartCmd()

	case secOut, secChan:
		if !m.ap.selectRow() {
			m.ok("%s: %s (unchanged)", sectionNames[m.ap.section], m.ap.appliedName(m.ap.section))
			return nil
		}
		if m.ap.streaming() || m.ap.monitor {
			m.ap.prepareRestart()
			return m.ap.restartCmd()
		}
		// nothing streaming: a playback/channel pick is stored for the next
		// session instead of force-starting capture
		m.ok("%s: %s", sectionNames[m.ap.section], m.ap.appliedName(m.ap.section))
		return nil
	}
	return nil
}

// openPortCmd / closePortCmd wrap monitor stream actions in tea.Cmds.

func openPortCmd(m *midi.Monitor, num int) tea.Cmd {
	return func() tea.Msg {
		if err := m.Open(num); err != nil {
			return portOpenErrMsg{num: num, err: err}
		}
		return portOpenedMsg{num: num}
	}
}

func closePortCmd(m *midi.Monitor, num int) tea.Cmd {
	return func() tea.Msg {
		m.Stop()
		return portClosedMsg{num: num}
	}
}

// toggleMonitor switches the capture→playback monitor path. Turning it on
// re-opens both ends (device consistency); turning it off stops playback only
// so capture keeps metering.
func (m Model) toggleMonitor() (tea.Model, tea.Cmd) {
	if m.ap.eng == nil {
		m.fail("audio unavailable: %v", m.ap.initErr)
		return m, nil
	}
	m.ap.monitor = !m.ap.monitor
	if m.ap.monitor {
		m.ap.monOn.Store(false)
		m.ap.prepareRestart()
		m.ok("audio monitor on: %s → %s", m.ap.inName(), m.ap.outName())
		return m, m.ap.restartCmd()
	}
	if m.ap.playStream != nil {
		m.ap.playStream.Stop()
		m.ap.playStream = nil
	}
	m.ap.monOn.Store(false)
	m.ok("audio: monitor off")
	return m, nil
}

// updateEvent keeps the counters fresh, keeps the subscription alive, and
// lights the in-lamp. Traffic shows as counters plus the activity lamps.
func (m Model) updateEvent(ev midi.Event) (tea.Model, tea.Cmd) {
	m.events++
	if m.lamps != nil {
		m.lamps.in.Store(time.Now().UnixNano())
	}

	// Clock spam gets its own counter to keep counters meaningful. (Match on
	// the parsed message type: ev.String() carries a timestamp prefix, so a
	// string compare silently counted nothing.)
	if ev.Msg.Type() == gomidi.TimingClockMsg {
		m.clocks++
	} else {
		m.dropped = m.monitor.Dropped()
	}
	return m, m.lamps.withLampOff(waitForEventCmd(m.monitor.Events()))
}

// withLampOff schedules the lamp-dousing repaint if none is pending yet;
// cmd is otherwise untouched (returned as-is when one is).
func (l *lamps) withLampOff(cmd tea.Cmd) tea.Cmd {
	if l == nil {
		return cmd
	}
	if l.blinking.Swap(true) {
		return cmd // a repaint is already scheduled
	}
	if cmd == nil {
		return lampOffCmd()
	}
	return tea.Batch(cmd, lampOffCmd())
}

// ---- update helpers ---------------------------------------------------

func (m *Model) ok(format string, args ...any) {
	m.status = fmt.Sprintf(format, args...)
	m.isErr = false
}

func (m *Model) fail(format string, args ...any) {
	m.status = fmt.Sprintf(format, args...)
	m.isErr = true
}

// compile-time interface check: Model must implement tea.Model.
var _ tea.Model = Model{}
