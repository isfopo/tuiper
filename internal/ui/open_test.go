package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tuiper/internal/midi"
)

// TestOpenPortNoPanic drives the real open-port sequence without a TUI: list
// ports, press Enter, process the portOpenedMsg. On Windows this needs real
// ports via the rtmidi engine (CGO build); with the test engine it uses
// loopback ports. Regression: the monitor used to be created inside
// togglePort, a value method, so the assignment never reached the model —
// handing portOpenedMsg then panicked on a nil monitor and killed the TUI.
func TestOpenPortNoPanic(t *testing.T) {
	eng, err := midi.NewTestEngine()
	if err != nil {
		t.Fatalf("test engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	var model tea.Model = New(eng, false)

	initCmd := model.Init()
	portsLoaded := initCmd()
	model, _ = model.Update(portsLoaded)

	var openCmd tea.Cmd
	model, openCmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if openCmd == nil {
		t.Fatal("enter produced no open command (no ports listed)")
	}

	opened := openCmd()
	if _, ok := opened.(portOpenErrMsg); ok {
		t.Fatalf("open failed: %v", opened)
	}

	// This next call is the regression: Update on portOpenedMsg dereferences
	// the monitor; with the old code m.monitor was nil and it panicked.
	model, _ = model.Update(opened)

	if m := model.(Model); m.monitor == nil {
		t.Fatal("model monitor is nil after open sequence")
	}
}
