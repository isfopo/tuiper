package ui

import (
	"sync"
	"time"

	gomidi "gitlab.com/gomidi/midi/v2"

	engmidi "tuiper/internal/midi"
)

// DemoSender injects synthetic MIDI events through a loopback (engine out
// port -> engine in port) so the monitor can be exercised without hardware.
// It only works with a testdrv based engine (-engine test).
type DemoSender struct {
	eng      *engmidi.Engine
	outPort  int
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewDemoSender creates the synthetic event sender for a test engine.
func NewDemoSender(eng *engmidi.Engine) *DemoSender {
	return &DemoSender{
		eng:     eng,
		outPort: 0,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start runs the demo loop in the background. Non-blocking.
func (d *DemoSender) Start() {
	go d.run()
}

// Stop signals the loop to end and returns a channel that closes when the
// loop goroutine has finished. Idempotent.
func (d *DemoSender) Stop() <-chan struct{} {
	d.stopOnce.Do(func() { close(d.stop) })
	return d.done
}

// run sends a small repeating pattern:
//
//	t+0.0s  NoteOn  C-2            (a "pedal press" style stomp)
//	t+0.2s  NoteOff C-2
//	t+0.7s  CC 64 (sustain) 127
//	t+1.2s  CC 64 (sustain) 0
//	t+1.5s  ProgramChange, pitch bend sweep
func (d *DemoSender) run() {
	defer close(d.done)

	out, err := d.eng.Out(d.outPort)
	if err != nil {
		return
	}
	if err := out.Open(); err != nil { // testdrv requires an explicit open
		return
	}

	send := func(msg gomidi.Message) {
		_ = out.Send(msg) // best effort; demo port errors are not fatal
	}

	pattern := func() {
		send(gomidi.NoteOn(0, 36, 96)) // ch1 C-2
		time.Sleep(200 * time.Millisecond)
		send(gomidi.NoteOn(0, 36, 0)) // note off via velocity 0
		time.Sleep(500 * time.Millisecond)
		send(gomidi.ControlChange(0, 64, 127)) // sustain pedal down
		time.Sleep(500 * time.Millisecond)
		send(gomidi.ControlChange(0, 64, 0)) // sustain pedal up
		time.Sleep(300 * time.Millisecond)
		send(gomidi.ProgramChange(0, 1))
		for i := 0; i <= 10; i++ {
			send(gomidi.Pitchbend(0, int16(i*500)))
		}
	}

	pattern() // run immediately so there is something to see

	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-d.stop:
			return
		case <-tick.C:
			pattern()
		}
	}
}
