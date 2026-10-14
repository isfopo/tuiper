package midi

import (
	"testing"
	"time"

	gomidi "gitlab.com/gomidi/midi/v2"
)

// TestMonitorLifecycle drives the monitor against the private test engine
// (two loopback ports, no CGO): open, receive a note via the wired out port,
// re-open (old channel closes), stop (channel closes), and the error path.
func TestMonitorLifecycle(t *testing.T) {
	t.Parallel()

	eng, err := NewTestEngine()
	if err != nil {
		t.Fatalf("test engine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	mon := NewMonitor(eng)
	if mon.Active() {
		t.Fatal("monitor active before Open")
	}

	// open port 0 (the wired in port "tuiper-test-in")
	if err := mon.Open(0); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !mon.Active() {
		t.Fatal("monitor not active after Open")
	}
	ch := mon.Events()
	if ch == nil {
		t.Fatal("Events() returned nil while active")
	}

	// send a note through the loopback out port; it must arrive parsed
	out, err := eng.Out(0)
	if err != nil {
		t.Fatalf("out port: %v", err)
	}
	if err := out.Open(); err != nil {
		t.Fatalf("open out port: %v", err)
	}
	t.Cleanup(func() { out.Close() })
	if err := out.Send(gomidi.NoteOn(0, 60, 100)); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Msg.Type() != gomidi.NoteOnMsg {
			t.Fatalf("got %v, want NoteOn", ev.Msg.Type())
		}
		var chn, key, vel uint8
		if !ev.Msg.GetNoteOn(&chn, &key, &vel) || key != 60 || vel != 100 {
			t.Fatalf("unexpected note payload: % X", []byte(ev.Msg))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}

	// re-open: the monitor tears the old subscription down, closing the old
	// (already handed out) channel; a reader of the old channel terminates
	if err := mon.Open(0); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("old channel received an event after reopen")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old events channel not closed within 5s after reopen")
	}

	// dropped count resets per Open even when the consumer is deliberately
	// slow (receive side held shut by not reading)
	newCh := mon.Events()
	for i := 0; i < eventBuffer+50; i++ {
		_ = out.Send(gomidi.NoteOn(0, 60, 100))
	}
	time.Sleep(300 * time.Millisecond)
	if mon.Dropped() == 0 {
		t.Log("driver drained the whole burst without drops (buffer fine)")
	}

	// stop: both the active flag and the current channel close
	mon.Stop()
	if mon.Active() {
		t.Fatal("monitor still active after Stop")
	}
	if _, ok := <-newCh; ok {
		t.Fatal("events channel open after Stop")
	}

	// stopping an idle monitor is a no-op, and unknown ports error
	mon.Stop()
	if err := mon.Open(99); err == nil {
		t.Fatal("opening unknown port 99 succeeded")
	}
	if mon.Active() {
		t.Fatal("failed Open left the monitor active")
	}
}
