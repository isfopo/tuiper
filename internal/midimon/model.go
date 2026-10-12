// Package midimon implements the first tuiper milestone: a Bubble Tea based
// MIDI monitor. It lists MIDI input ports, lets you open one, and scrolls
// incoming events live. Keyboard only, no mouse, no config files yet.
//
// Structure notes (Elm architecture, as used by Bubble Tea):
//
//	Model   - all app state, plain struct
//	Init()  - commands to run at startup
//	Update() - (model, incoming message) -> (model, followup commands)
//	View()  - render current state to a string
//
// The MIDI hardware details live in internal/midi; this package only knows
// about ports, events, and its own state.
package midimon

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"tuiper/internal/midi"
)

// logCap is the maximum number of retained event lines. When long running,
// older lines are simply replaced (simple sliding window; a proper scroll
// buffer comes with the looper session view).
const logCap = 512

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

	log     []string
	events  uint64 // total received
	clocks  uint64 // timing clock / tick, counted separately (often spammy)
	dropped uint64 // dropped by the monitor buffer
	status  string // last status line
	isErr   bool
}

// New returns a fresh monitor model. eng chooses the MIDI driver to work
// with; demo enables the synthetic event sender (test engine only).
func New(eng *midi.Engine, demo bool) Model {
	return Model{
		eng:     eng,
		demo:    demo,
		openNum: -1,
		status:  "ready",
		height:  24,
		width:   80,
	}
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

		case "q", "esc", "ctrl+c":
			return m, tea.Quit

		case "up", "k":
			if len(m.ports) > 0 {
				m.cursor = (m.cursor - 1 + len(m.ports)) % len(m.ports)
			}
			return m, nil

		case "down", "j":
			if len(m.ports) > 0 {
				m.cursor = (m.cursor + 1) % len(m.ports)
			}
			return m, nil

		case "enter", "space":
			return m, m.togglePort()

		case "r":
			return m, m.portsCmd()
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
	}

	return m, nil
}

// updateEvent appends a formatted event and keeps the subscription alive.
func (m Model) updateEvent(ev midi.Event) (tea.Model, tea.Cmd) {
	m.events++

	line := ev.String()
	// Clock spam gets its own counter to keep the log useful
	if line == "TimingClock" {
		m.clocks++
		return m, waitForEventCmd(m.monitor.Events())
	}
	m.log = append(m.log, line)
	if len(m.log) > logCap {
		m.log = m.log[len(m.log)-logCap:]
	}
	m.dropped = m.monitor.Dropped()
	return m, waitForEventCmd(m.monitor.Events())
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
		if m.demo && m.demoCtl == nil {
			m.demoCtl = NewDemoSender(m.eng)
		}
		return closePortCmd(m.monitor, num)
	}
	if m.demo && m.demoCtl == nil {
		m.demoCtl = NewDemoSender(m.eng)
	}
	return openPortCmd(m.monitor, num)
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
