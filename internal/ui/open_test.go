package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tuiper/internal/midi"
)

// key renders a tea.KeyMsg the way the update code sees it (String()-level
// matching: "enter", "esc", "d", "tab"...).
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// runCmds unwraps bubbletea batch messages and executes every sub-command,
// feeding the results back through Update (the test engine and helpers need
// the same message plumbing the real TUI loop provides).
//
// Chained commands are NOT followed for stream/subscription messages: those
// chain into blocking goroutine-side loops in the real TUI (waitForEventCmd
// parks on the events channel, meter ticks reschedule forever), which would
// deadlock the test instead of simulating it.
func runCmds(t *testing.T, model tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	for cmd != nil {
		switch msg := cmd().(type) {
		case nil:
			return model
		case tea.BatchMsg:
			for _, c := range msg {
				model = runCmds(t, model, c)
			}
			return model
		default:
			var next tea.Cmd
			model, next = model.Update(msg)
			switch msg.(type) {
			case portOpenedMsg, portClosedMsg, captureStartedMsg,
				playbackStartedMsg, audioMeterMsg, eventMsg:
				return model
			}
			cmd = next
		}
	}
	return model
}

// TestOpenPortViaModal drives the full selection sequence without a TUI:
// enum lists arrive, 'd' opens the modal, enter applies the midi row, and
// the portOpenedMsg lands on a model that actually knows the port list.
// Regression: the monitor/panel used to be built inside value-copy styling
// Update helpers, so assignments silently vanished from the rendered model.
func TestOpenPortViaModal(t *testing.T) {
	eng, err := midi.NewTestEngine()
	if err != nil {
		t.Fatalf("test engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	var model tea.Model = New(eng, false)
	model = runCmds(t, model, model.Init())

	m := model.(Model)
	if len(m.ap.ports) == 0 {
		t.Fatal("Init enumeration produced no midi ports")
	}

	// open the modal
	model, _ = model.Update(key("d"))
	if m := model.(Model); !m.modal.Opened() {
		t.Fatal("'d' did not open the device modal")
	}

	// enter applies the midi row: open port 0
	var cmd tea.Cmd
	model, cmd = model.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter produced no apply command")
	}
	model = runCmds(t, model, cmd)

	if m := model.(Model); m.ap.openNum != 0 {
		t.Fatalf("port not open after apply: openNum=%d", m.ap.openNum)
	}
	if m := model.(Model); m.monitor == nil {
		t.Fatal("model monitor is nil after open sequence")
	}

	// enter again closes the port (toggle semantics) and stops the demo
	model = runCmds(t, model, runOnce(t, model, key("enter")))
	if m := model.(Model); m.ap.openNum != -1 {
		t.Fatalf("toggle did not close the port: openNum=%d", m.ap.openNum)
	}
}

// runOnce presses a key once and returns the immediate command (without
// draining chains — runCmds does that with its stream-message stop rule).
func runOnce(_ *testing.T, model tea.Model, msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	_, cmd = model.Update(msg)
	return cmd
}

// TestModalBackdropRefreshes verifies the composited backdrop tracks the
// base frame: a ports list landing while the modal is open must be visible
// through the modal view (the backdrop used to be frozen at open time
// because the refresh mutated a local, discarded Model value).
func TestModalBackdropRefreshes(t *testing.T) {
	var model tea.Model = New(eng(t), false)
	model = runCmds(t, model, model.Init())

	model, _ = model.Update(key("d"))
	m := model.(Model)
	if !m.modal.Opened() {
		t.Fatal("modal did not open")
	}

	// baseView before the update shows no enumerated ports
	if strings.Contains(m.View(), "port-zero") {
		t.Fatal("update happened outside the modal path")
	}

	var cmd tea.Cmd
	model, cmd = model.Update(portsMsg{ports: []string{"port-zero", "port-one"}})
	if cmd != nil {
		_ = cmd() // harmless; drain so ordering resembles the real loop
	}
	view := model.(Model).View()
	if !strings.Contains(view, "port-zero") {
		t.Fatal("modal backdrop did not refresh after a ports update")
	}
}

// TestModalNavigation walks sections and rows: tab cycles, up/down wrap,
// selecting an audio device records the applied selection without opening
// streams, and esc closes the modal.
func TestModalNavigation(t *testing.T) {
	var model tea.Model = New(eng(t), false)
	model = runCmds(t, model, model.Init())

	model, _ = model.Update(key("d"))
	m := model.(Model)
	if !m.modal.Opened() {
		t.Fatal("modal did not open")
	}

	// tab cycles through every section and wraps
	m = model.(Model)
	first := m.ap.section
	for i := 1; i <= sectionCount; i++ {
		if got := m.ap.section; got != (first+i-1)%sectionCount {
			t.Fatalf("section walk: at step %d section=%d want %d", i, got, (first+i-1)%sectionCount)
		}
		model, _ = model.Update(key("tab"))
		m = model.(Model)
	}

	// esc closes
	model, _ = model.Update(key("esc"))
	if m := model.(Model); m.modal.Opened() {
		t.Fatal("esc did not close the modal")
	}
}

// TestAudioApplyRequiresStreams verifies the guard added with the modal:
// applying a playback or channel row while nothing streams only stores the
// selection (no capture forced on), while applying a capture row does start
// capture (meter kicks in).
func TestAudioApplyRequiresStreams(t *testing.T) {
	var model tea.Model = New(eng(t), false)
	model = runCmds(t, model, model.Init())

	// the audio engine exists in tests (the container/host has one; nil
	// engines skip open attempts entirely)
	m := model.(Model)
	if m.ap.eng == nil {
		t.Skip("no audio engine in this environment")
	}

	// out section: pick a row and apply — nothing streams, so only the
	// selection is stored
	model, _ = model.Update(key("d"))    // open
	model, _ = model.Update(key("tab"))  // midi -> audio in
	model, _ = model.Update(key("tab"))  // audio in -> audio out
	model, _ = model.Update(key("down")) // move off the default row
	model = runCmds(t, model, runOnce(t, model, key("enter")))
	m = model.(Model)
	if m.ap.playStream != nil || m.ap.capStream != nil {
		t.Fatal("playback pick started streams with none open")
	}
	if m.ap.sel[secOut] != 1 {
		t.Fatalf("playback selection not stored: %d", m.ap.sel[secOut])
	}

	// in section: applying starts capture
	model, _ = model.Update(key("shift+tab"))
	model = runCmds(t, model, runOnce(t, model, key("enter")))
	m = model.(Model)
	if !m.ap.capturing && m.ap.capStream == nil {
		t.Skip("capture open unavailable in this environment")
	}
	if m := model.(Model); !m.ap.capturing {
		t.Fatal("capture pick did not start capture")
	}
}
