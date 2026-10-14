package ui

import (
	"testing"

	"tuiper/internal/audio"
)

// TestDropdownNavigation drives the dropdown state machine without any audio
// backend: default entry first, navigation wraps, pick records the target
// selection and reports whether it actually moved.
func TestDropdownNavigation(t *testing.T) {
	ap := &audioPanel{}
	ap.ins = []audio.Device{{Name: "A"}, {Name: "B"}}
	ap.outs = []audio.Device{{Name: "Out"}}

	// options list: synthetic "system default" first
	opts := ap.inOptions()
	if len(opts) != 3 || opts[0].Name != "system default" {
		t.Fatalf("inOptions = %+v, want default + 2 entries", opts)
	}
	if deviceIDFor(opts, 0) != nil {
		t.Fatal("default selection must map to nil DeviceID")
	}

	// open in-dropdown: cursor starts on the current selection (default)
	ap.toggleDropdown(dropIn)
	if ap.focus != dropIn || ap.cursor != 0 || ap.dropdownCount() != 3 {
		t.Fatalf("open state: focus=%d cursor=%d count=%d", ap.focus, ap.cursor, ap.dropdownCount())
	}

	// wrap-around navigation
	ap.moveCursor(-1)
	if ap.cursor != 2 {
		t.Fatalf("cursor %d, want wrapped to 2", ap.cursor)
	}
	ap.moveCursor(2)
	if ap.cursor != 1 {
		t.Fatalf("cursor %d, want 1", ap.cursor)
	}

	// pick selects and reports the change
	if !ap.pick() {
		t.Fatal("pick should report a changed selection")
	}
	if ap.inSel != 1 || ap.focus != dropNone {
		t.Fatalf("after pick: inSel=%d focus=%d", ap.inSel, ap.focus)
	}

	// reopening starts on the stored selection; picking it again changes nothing
	ap.toggleDropdown(dropIn)
	if ap.cursor != 1 {
		t.Fatalf("cursor %d, want restored selection 1", ap.cursor)
	}
	if ap.pick() {
		t.Fatal("re-picking the same entry should report no change")
	}

	// channels dropdown cycles stereo -> mono
	ap.toggleDropdown(dropChan)
	ap.moveCursor(1)
	if !ap.pick() || ap.chans != chanMono {
		t.Fatalf("channels pick: chans=%d", ap.chans)
	}
	if ap.channels() != 1 {
		t.Fatalf("channels() = %d, want 1", ap.channels())
	}

	// out-dropdown with 2 entries clamps: esc closes without side effects
	ap.toggleDropdown(dropOut)
	if ap.dropdownCount() != 2 {
		t.Fatalf("out dropdown count = %d, want 2", ap.dropdownCount())
	}
	ap.cursor = 0
	if ap.pick() {
		t.Fatal("re-picking default out should report no change")
	}

	if audio.Peak([]int16{3, -5, 2}) != 5 {
		t.Fatal("audio.Peak should give the max magnitude")
	}
}
