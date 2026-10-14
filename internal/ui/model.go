// Package ui implements the tuiper terminal UI: a Bubble Tea based monitor
// for MIDI and audio devices. It lists MIDI input ports (open one and watch
// events scroll by) and audio endpoints (device/channel dropdowns, capture
// meter, monitor path). Keyboard only, no mouse, no config files yet.
//
// Structure notes (Elm architecture, as used by Bubble Tea):
//
//	Model   - all app state, plain struct
//	Init()  - commands to run at startup
//	Update() - (model, incoming message) -> (model, followup commands)
//	View()  - render current state to a string
//
// Driver details live in internal/midi and internal/audio; this package
// only knows about ports/devices, events, and its own state.
package ui

import (
	"fmt"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiper/internal/midi"
)

// lamps are the upper-right MIDI activity indicators (dim by default, lit
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

// logCap is the maximum number of retained event lines. When long running,
// older lines are simply replaced (simple sliding window; a proper scroll
// buffer comes with the looper session view).
const logCap = 512

// blinkWindow is how long a lamp stays lit after observed MIDI traffic.
const blinkWindow = 150 * time.Millisecond

// Model holds all state of the monitor.
type Model struct {
	width, height int

	eng    *midi.Engine // owns the MIDi
	ports  []string     // input port names; index == port number
	cursor int          // selected row in ports

	monitor *midi.Monitor
	openNum int // port number of the active listener, -1 when closed

	demo    bool // -demo mode: synthetic events on the test engine
	demoCtl *DemoSender

	ap *audioPanel // audio device dropdowns / meter / monitor (always non-nil after New)

	// MIDI activity lamps: receive/send stamps (unix nanos) written via
	// atomics from whichever goroutine observes traffic; View decides bright
	// vs. dark from the elapsed time since the stamp.
	lamps *lamps

	log     []string
	events  uint64 // total received
	clocks  uint64 // timing clock / tick, counted separately (often spammy)
	dropped uint64 // dropped by the monitor buffer
	status  string // last status line
	isErr   bool
}

// New returns a fresh monitor model. eng chooses the MIDI driver to work
// with; demo enables the synthetic event sender (test engine only).
//
// The monitor is created here, not in togglePort: Update and togglePort work
// on value copies of Model, so an assignment inside togglePort would never
// reach the model the TUI keeps rendering (a nil-monitor panic on the first
// portOpenedMsg).
func New(eng *midi.Engine, demo bool) Model {
	m := Model{
		eng:     eng,
		monitor: midi.NewMonitor(eng),
		demo:    demo,
		ap:      newAudioPanel(),
		lamps:   newLamps(),
		openNum: -1,
		status:  "ready",
		height:  24,
		width:   80,
	}
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

func (m Model) portsCmd() tea.Cmd {
	return func() tea.Msg {
		ports, err := m.eng.InputPorts()
		return portsMsg{ports: ports, err: err}
	}
}

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
	return m.portsCmd()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {

		case "q", "ctrl+c":
			return m, tea.Quit

		case "esc":
			// esc first cancels an open dropdown, and only quits otherwise
			if m.ap != nil && m.ap.focus != dropNone {
				m.ap.focus = dropNone
				return m, nil
			}
			return m, tea.Quit

		case "up", "k":
			if m.ap != nil && m.ap.focus != dropNone {
				m.ap.moveCursor(-1)
				return m, nil
			}
			if len(m.ports) > 0 {
				m.cursor = (m.cursor - 1 + len(m.ports)) % len(m.ports)
			}
			return m, nil

		case "down", "j":
			if m.ap != nil && m.ap.focus != dropNone {
				m.ap.moveCursor(1)
				return m, nil
			}
			if len(m.ports) > 0 {
				m.cursor = (m.cursor + 1) % len(m.ports)
			}
			return m, nil

		case "enter", "space":
			if m.ap != nil && m.ap.focus != dropNone {
				changed := m.ap.pick()
				if changed {
					m.ap.prepareRestart()
					return m, m.ap.restartCmd()
				}
				return m, nil
			}
			return m, m.togglePort()

		case "i", "o", "c":
			if m.ap == nil {
				return m, nil
			}
			if m.ap.eng == nil {
				m.fail("audio unavailable: %v", m.ap.initErr)
				return m, nil
			}
			m.ap.toggleDropdown(map[string]int{"i": dropIn, "o": dropOut, "c": dropChan}[msg.String()])
			if m.ap.ins == nil && m.ap.outs == nil {
				// first audio interaction: enumerate in the background;
				// the dropdown opens immediately and fills on arrival
				return m, m.ap.listCmd()
			}
			return m, nil

		case "m":
			return m.toggleMonitor()

		case "r":
			cmds := []tea.Cmd{m.portsCmd()}
			if m.ap != nil {
				cmds = append(cmds, m.ap.listCmd())
			}
			return m, tea.Batch(cmds...)
		}

	case portsMsg:
		if msg.err != nil {
			m.fail("list ports: %v", msg.err)
			return m, nil
		}
		m.ports = msg.ports
		if m.cursor >= len(m.ports) {
			m.cursor = 0
		}
		return m, nil

	case portOpenedMsg:
		m.openNum = msg.num
		m.ok("listening: %s", m.ports[msg.num])
		cmds := []tea.Cmd{waitForEventCmd(m.monitor.Events())}
		if m.demo && m.demoCtl != nil {
			m.demoCtl.Start()
		}
		return m, tea.Batch(cmds...)

	case portOpenErrMsg:
		m.fail("open port %d: %v", msg.num, msg.err)
		return m, nil

	case portClosedMsg:
		m.openNum = -1
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
		if m.ap == nil {
			return m, nil
		}
		m.ap.ins = msg.ins
		m.ap.outs = msg.outs
		// re-clamp stored selections against the new lists
		if m.ap.inSel > len(msg.ins) {
			m.ap.inSel = 0
		}
		if m.ap.outSel > len(msg.outs) {
			m.ap.outSel = 0
		}
		m.ok("audio: %d capture / %d playback devices", len(msg.ins), len(msg.outs))
		return m, nil

	case audioErrMsg:
		if m.ap == nil {
			return m, nil
		}
		m.fail("audio: %v", msg.err)
		return m, nil

	case captureStartedMsg:
		if m.ap == nil {
			return m, nil
		}
		m.ap.capStream = msg.stream
		m.ap.capturing = true
		// the monitor gate opens fully only once playback is up too
		m.ap.monOn.Store(m.ap.monitor && m.ap.playStream != nil)
		m.ok("audio capture: %s (%d ch)", m.ap.inName(), m.ap.channels())
		return m, meterTickCmd()

	case playbackStartedMsg:
		if m.ap == nil {
			return m, nil
		}
		m.ap.playStream = msg.stream
		m.ap.monOn.Store(m.ap.monitor)
		m.ok("audio monitor: %s → %s", m.ap.inName(), m.ap.outName())
		return m, nil

	case audioMeterMsg:
		if m.ap == nil {
			return m, nil
		}
		m.ap.takeMeter()
		if m.ap.capturing {
			return m, meterTickCmd()
		}
		return m, nil
	}

	return m, nil
}

// updateEvent appends a formatted event, keeps the subscription alive, and
// lights the in-lamp.
func (m Model) updateEvent(ev midi.Event) (tea.Model, tea.Cmd) {
	m.events++
	if m.lamps != nil {
		m.lamps.in.Store(time.Now().UnixNano())
	}

	line := ev.String()
	// Clock spam gets its own counter to keep the log useful
	if line == "TimingClock" {
		m.clocks++
		return m, m.lamps.withLampOff(waitForEventCmd(m.monitor.Events()))
	}
	m.log = append(m.log, line)
	if len(m.log) > logCap {
		m.log = m.log[len(m.log)-logCap:]
	}
	m.dropped = m.monitor.Dropped()
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

// togglePort opens the selected port, or closes the active one.
func (m Model) togglePort() tea.Cmd {
	if len(m.ports) == 0 {
		m.fail("no input ports available")
		return nil
	}

	if m.monitor == nil {
		m.monitor = midi.NewMonitor(m.eng)
	}

	num := m.cursor
	if m.openNum == num {
		return closePortCmd(m.monitor, num)
	}
	return openPortCmd(m.monitor, num)
}

// toggleMonitor switches the capture→playback monitor path. Turning it on
// re-opens both ends (device consistency); turning it off stops playback only
// so capture keeps metering.
func (m Model) toggleMonitor() (tea.Model, tea.Cmd) {
	if m.ap == nil {
		return m, nil
	}
	if m.ap.eng == nil {
		m.fail("audio unavailable: %v", m.ap.initErr)
		return m, nil
	}
	m.ap.monitor = !m.ap.monitor
	if m.ap.monitor {
		m.ap.monOn.Store(false)
		m.ap.prepareRestart()
		m.ok("audio monitor on (beware feedback on real hardware)")
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
