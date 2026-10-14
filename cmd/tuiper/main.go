// Command tuiper is a looper built around a terminal user interface. It is
// currently past milestone 1 (MIDI input monitor; -watch) and exercising
// milestone 2 (audio device enumeration + capture meter + loopback round
// trip; -audio).
//
// Usage:
//
//	tuiper                       real hardware ports (RtMidi via CGO)
//	tuiper -engine test          fake loopback ports (no hardware needed)
//	tuiper -engine test -demo    fake ports plus a synthetic event sender
//	tuiper -audio 3s             headless audio device check
//	tuiper -audio 3s -loopback   capture fed straight into playback
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiper/internal/midi"
	"tuiper/internal/ui"
)

func main() {
	engineName := flag.String("engine", "rtmidi", "midi driver: rtmidi | test")
	demo := flag.Bool("demo", false, "inject synthetic events (engine test only)")
	watch := flag.Duration("watch", 0, "headless mode: print events for the given duration")
	audioDur := flag.Duration("audio", 0, "headless audio check: enumerate devices and meter capture (milestone 2)")
	loopback := flag.Bool("loopback", false, "play captured audio back out (-audio mode; feedback risk on real hardware!)")
	inline := flag.Bool("inline", false, "run the TUI inline (no alternate screen; try this if the top line is missing in your terminal)")
	flag.Parse()

	if *audioDur > 0 {
		// milestone 2 check: no MIDI engine needed at all
		os.Exit(runAudio(*audioDur, *loopback))
	}

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

	model := ui.New(eng, *demo)
	program := tea.NewProgram(model)
	if !*inline {
		// alternate screen is the normal TUI presentation; -inline opts out
		// for terminals whose alt-screen handling is buggy (e.g. Zed's
		// integrated terminal has hidden the top line of altscreen apps).
		program = tea.NewProgram(model, tea.WithAltScreen())
	}

	final, err := program.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper:", err)
		os.Exit(1)
	}

	// tear down the MIDI state owned by the final model, then the driver
	if m, ok := final.(ui.Model); ok {
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
		s := ui.NewDemoSender(eng)
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
