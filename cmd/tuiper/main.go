// Command tuiper is a looper built around a terminal user interface. It is
// currently in milestone 1: a MIDI input monitor (see internal/midimon).
//
// Usage:
//
//	tuiper                       real hardware ports (RtMidi via CGO)
//	tuiper -engine test          fake loopback ports (no hardware needed)
//	tuiper -engine test -demo    fake ports plus a synthetic event sender
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiper/internal/midi"
	"tuiper/internal/midimon"
)

func main() {
	engineName := flag.String("engine", "rtmidi", "midi driver: rtmidi | test")
	demo := flag.Bool("demo", false, "inject synthetic events (engine test only)")
	watch := flag.Duration("watch", 0, "headless mode: print events for the given duration")
	flag.Parse()

	eng, err := newEngine(*engineName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper:", err)
		os.Exit(1)
	}

	if *watch > 0 {
		code := runWatch(eng, *demo, *watch)
		_ = eng.Close()
		os.Exit(code)
	}

	model := midimon.New(eng, *demo)
	program := tea.NewProgram(model,
		tea.WithAltScreen(),
	)

	final, err := program.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper:", err)
		os.Exit(1)
	}

	// tear down the MIDI state owned by the final model, then the driver
	if m, ok := final.(midimon.Model); ok {
		e := m.Shutdown()
		_ = e.Close()
	} else {
		_ = eng.Close()
	}
}

// runWatch opens the first input port and prints events to stdout for the
// given duration (a TUI-free smoke test and debugging tool).
func runWatch(eng *midi.Engine, demo bool, d time.Duration) int {
	if demo {
		s := midimon.NewDemoSender(eng)
		s.Start()
		defer func() {
			<-s.Stop()
		}()
	}

	mon := midi.NewMonitor(eng)
	defer mon.Stop()

	if err := mon.Open(0); err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: open port 0:", err)
		return 1
	}

	fmt.Printf("watching %s port 0 for %s...\n", eng.Name(), d)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	for {
		select {
		case ev, ok := <-mon.Events():
			if !ok {
				return 0
			}
			fmt.Println(ev.String())
		case <-ctx.Done():
			fmt.Println("watch ended")
			return 0
		}
	}
}

func newEngine(name string) (*midi.Engine, error) {
	switch name {
	case "rtmidi":
		return midi.NewRtMidiEngine()
	case "test":
		return midi.NewTestEngine()
	default:
		return nil, fmt.Errorf("unknown engine %q (want rtmidi | test)", name)
	}
}
