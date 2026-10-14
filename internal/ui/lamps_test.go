package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"tuiper/internal/midi"
)

// renderANSI pins a color profile for the test process: without a terminal
// lipgloss renders unstyled, and the lit/dark lamp strings would compare
// equal regardless of the logic under test.
func renderANSI(t *testing.T) {
	t.Helper()
	lipgloss.SetColorProfile(termenv.ANSI256)
}

// TestLampBlinkLogic verifies the in/out lamps: dark by default, lit inside
// the blink window of a stamp, dark again once it expires.
func TestLampBlinkLogic(t *testing.T) {
	renderANSI(t)
	l := newLamps()

	// timing logic
	if l.lit(&l.in) {
		t.Fatal("in lamp lit without traffic")
	}
	l.in.Store(time.Now().UnixNano())
	if !l.lit(&l.in) {
		t.Fatal("in lamp did not light on a fresh stamp")
	}
	l.in.Store(time.Now().Add(-2 * blinkWindow).UnixNano())
	if l.lit(&l.in) {
		t.Fatal("in lamp did not expire after the blink window")
	}

	// view-level: lit renders differently than dark (both dots present)
	var model Model = New(eng(t), false)
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	dark := model.lampView()
	model.lamps.out.Store(time.Now().UnixNano())
	lit := model.lampView()
	if lit == dark {
		t.Fatal("out lamp render did not change when lit")
	}
	model.lamps.out.Store(time.Now().Add(-2 * blinkWindow).UnixNano())
	if got := model.lampView(); got != dark {
		t.Fatal("out lamp render did not return to dark")
	}
}

// eng creates a throwaway test MIDI engine for lamp view tests.
func eng(t *testing.T) *midi.Engine {
	t.Helper()
	e, err := midi.NewTestEngine()
	if err != nil {
		t.Fatalf("test engine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// TestLampOffPipeline drives the debounce: withLampOff schedules exactly one
// dousing repaint while the window is pending, the lampOffMsg clears it, and
// then a new one can be scheduled.
func TestLampOffPipeline(t *testing.T) {
	l := newLamps()

	if first := l.withLampOff(nil); first == nil {
		t.Fatal("first call must schedule the lamp-off repaint")
	}
	if again := l.withLampOff(nil); again != nil {
		t.Fatal("second call within the window must not schedule another repaint")
	}

	// updateEvent integration: receiving an event (via copy paths) still
	// schedules through the shared lamp state
	model := New(eng(t), false)
	_, cmd := model.updateEvent(midi.Event{})
	if cmd == nil {
		t.Fatal("updateEvent should return at least the wait-for-event cmd")
	}
	if !model.lamps.blinking.Load() {
		t.Fatal("blinking flag not set while a lamp-off repaint is pending")
	}

	// the repaint message clears the flag (on the returned copy, which the
	// runtime keeps — the lamps pointer itself is shared, as always)
	upd, _ := model.Update(lampOffMsg{})
	purged := upd.(Model)
	if purged.lamps.blinking.Load() {
		t.Fatal("lampOffMsg did not clear the pending flag")
	}

	// ... allowing a fresh schedule afterwards
	if fresh := model.lamps.withLampOff(nil); fresh == nil {
		t.Fatal("after dousing a new lamp-off repaint should be schedulable")
	}
}
