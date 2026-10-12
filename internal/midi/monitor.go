// Package midi wraps the realtime MIDI plumbing of tuiper (driver selection,
// port enumeration, listening) behind a small Go API. The intent is that the
// TUI never imports a CGO-bound driver directly and that later stages (e.g. a
// looper engine) can consume Event streams the same way the monitor does.
//
// Design notes:
//
//   - An Engine owns one drivers.Driver. Choosing the driver explicitly (rather
//     than via gomidi's global registry state) keeps port numbering stable even
//     when a second pure-Go driver is linked in for tests.
//   - Monitor binds one input port of the engine and forwards parsed messages
//     into a buffered channel.
//
// The real driver is RtMidi via gitlab.com/gomidi/rtmididrv, which needs CGO.
// On Windows build with CGO_ENABLED=1 and the zig cc wrappers in tools/ (see
// build.ps1).
package midi

import (
	"fmt"
	"sync"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv" // registers rtmididrv
)

// Event is one MIDI message as it arrived from an input port, annotated with
// the local receive time.
type Event struct {
	Received time.Time
	Msg      midi.Message
}

func (e Event) String() string {
	return fmt.Sprintf("%s  %s", e.Received.Format("15:04:05.000"), e.describe())
}

// describe renders the message in a fixed-width, human readable form.
// Note: MIDI channels are displayed 1-based, matching gear documentation.
func (e Event) describe() string {
	var ch, val1, val2 uint8

	switch e.Msg.Type() {
	case midi.NoteOnMsg:
		switch {
		case e.Msg.GetNoteStart(&ch, &val1, &val2):
			return fmt.Sprintf("NoteOn        ch%-2d %-4s vel%-3d", ch+1, midi.Note(val1).Name(), val2)
		case e.Msg.GetNoteEnd(&ch, &val1): // note_on with velocity 0
			return fmt.Sprintf("NoteOff       ch%-2d %-4s", ch+1, midi.Note(val1).Name())
		}
	case midi.NoteOffMsg:
		if e.Msg.GetNoteOff(&ch, &val1, &val2) {
			return fmt.Sprintf("NoteOff       ch%-2d %-4s vel%-3d", ch+1, midi.Note(val1).Name(), val2)
		}
	case midi.ControlChangeMsg:
		if e.Msg.GetControlChange(&ch, &val1, &val2) {
			name := midi.ControlChangeName[val1]
			return fmt.Sprintf("ControlChange ch%-2d cc%-3d %-13s val%-4d", ch+1, val1, name, val2)
		}
	case midi.PitchBendMsg:
		var rel int16
		if e.Msg.GetPitchBend(&ch, &rel, nil) {
			return fmt.Sprintf("PitchBend     ch%-2d rel%+5d", ch+1, rel)
		}
	case midi.ProgramChangeMsg:
		if e.Msg.GetProgramChange(&ch, &val1) {
			return fmt.Sprintf("ProgramChange ch%-2d prog%-3d", ch+1, val1)
		}
	case midi.AfterTouchMsg:
		if e.Msg.GetAfterTouch(&ch, &val1) {
			return fmt.Sprintf("AfterTouch    ch%-2d press%-3d", ch+1, val1)
		}
		return e.Msg.Type().String()
	case midi.TimingClockMsg:
		return "TimingClock"
	case midi.StartMsg:
		return "Start"
	case midi.StopMsg:
		return "Stop"
	case midi.ContinueMsg:
		return "Continue"
	case midi.ActiveSenseMsg:
		return "ActiveSense"
	}

	return fmt.Sprintf("%-13s % X", e.Msg.Type(), e.Msg)
}

// Engine owns one MIDI driver (and thus a set of ports).
type Engine struct {
	drv drivers.Driver
}

// NewRtMidiEngine returns the engine backed by the CGO RtMidi driver, which
// talks to the real ports of the OS (WinMM/CoreMIDI/ALSA).
func NewRtMidiEngine() (*Engine, error) {
	d := drivers.REGISTRY["rtmididrv"]
	if d == nil {
		return nil, fmt.Errorf("rtmididrv is not registered (missing import or CGO build?)")
	}
	return &Engine{drv: d}, nil
}

// Name returns the driver's name.
func (e Engine) Name() string { return e.drv.String() }

// In returns the n-th input port of the engine's driver.
func (e Engine) In(n int) (drivers.In, error) {
	ins, err := e.drv.Ins()
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= len(ins) {
		return nil, fmt.Errorf("unknown input port %d (%d ports total)", n, len(ins))
	}
	return ins[n], nil
}

// Out returns the n-th output port of the engine's driver.
func (e Engine) Out(n int) (drivers.Out, error) {
	outs, err := e.drv.Outs()
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= len(outs) {
		return nil, fmt.Errorf("unknown output port %d (%d ports total)", n, len(outs))
	}
	return outs[n], nil
}

// InputPorts lists the input port names; the index is the port number used
// by Monitor.Open and Engine.In.
func (e Engine) InputPorts() ([]string, error) {
	ins, err := e.drv.Ins()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(ins))
	for i, p := range ins {
		names[i] = p.String()
	}
	return names, nil
}

// Close releases driver resources. Call once at program end.
func (e Engine) Close() error { return e.drv.Close() }

// Monitor listens on a single MIDI input port and forwards parsed events
// into a buffered channel. It is safe to open/stop from the goroutine that
// owns the TUI; the driver calls back from its own goroutine.
type Monitor struct {
	eng       *Engine
	mu        sync.Mutex
	in        drivers.In // driver port handle
	stopFn    func()     // stops the driver callback loop
	events    chan Event
	receiving bool   // a driver listener is subscribed
	deliverOK bool   // false as soon as shutdown starts; closes channel race
	dropped   uint64 // events discarded because the consumer was too slow
}

const eventBuffer = 256

// NewMonitor returns event-receiving decor of the engine's ports. Nothing is
// opened until Open is called.
func NewMonitor(eng *Engine) *Monitor {
	return &Monitor{eng: eng}
}

// Events returns the receive side of the event stream. On stop (or reopen)
// the channel is closed, so a reader loop terminates cleanly. After Open it
// is a newly created channel, so re-fetch Events().
func (m *Monitor) Events() <-chan Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events
}

// Active reports whether a port is currently being listened to.
func (m *Monitor) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.receiving
}

// Dropped reports how many events were discarded because the buffer was full.
func (m *Monitor) Dropped() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}

// Open starts listening on the given input port number. Any previously open
// listener is shut down first, including closing its (already handed out)
// Events channel.
func (m *Monitor) Open(portNumber int) error {
	m.shutdown()

	in, err := m.eng.In(portNumber)
	if err != nil {
		return fmt.Errorf("can't get input port %d: %w", portNumber, err)
	}

	// fresh event channel and delivery armed before the subscription starts
	ch := make(chan Event, eventBuffer)
	m.mu.Lock()
	m.events = ch
	m.receiving = true
	m.deliverOK = true
	m.dropped = 0
	m.mu.Unlock()

	stop, err := midi.ListenTo(in, func(msg midi.Message, timestampms int32) {
		m.deliver(Event{Received: time.Now(), Msg: msg})
	})
	if err != nil {
		m.shutdown()
		in.Close()
		return fmt.Errorf("can't listen on input port %d: %w", portNumber, err)
	}

	m.mu.Lock()
	m.stopFn = stop
	m.in = in
	m.mu.Unlock()
	return nil
}

// Stop unconditionally tears down an active listener and closes the event
// channel. Calling it when idle is fine.
func (m *Monitor) Stop() { m.shutdown() }

// shutdown is the single path that unsubscribes and closes the channel.
// Order matters: deliverOK is flipped under the lock before close(ch), so
// no delivery goroutine can send on the channel after we close it.
func (m *Monitor) shutdown() {
	m.mu.Lock()
	stop, in := m.stopFn, m.in
	m.stopFn, m.in = nil, nil
	m.receiving = false
	m.deliverOK = false
	ch := m.events
	m.events = nil
	m.mu.Unlock()

	if stop != nil {
		stop()
	}
	if in != nil {
		in.Close()
	}
	if ch != nil {
		close(ch)
	}
}

func (m *Monitor) deliver(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.deliverOK {
		return
	}
	select {
	case m.events <- e:
	default:
		m.dropped++
	}
}
