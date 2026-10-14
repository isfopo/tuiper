// Headless audio checks (no TUI): enumerate devices, meter the default
// capture input for a duration, optionally round trip capture into playback
// (-loopback).
package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"tuiper/internal/audio"
)

// runAudio lists devices and meters the default capture input for d. With
// loopback, captured blocks are pushed into the default output as well —
// the full capture→playback round trip. NOTE: on real hardware (a mic into
// speakers) this makes a feedback loop; it is an explicit opt-in for that
// reason. In the container the rig routes capture to a null sink's monitor,
// so loopback is silent-safe.
func runAudio(d time.Duration, loopback bool) int {
	eng, err := audio.NewEngine()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: audio:", err)
		return 1
	}
	defer eng.Close()

	ins, err := eng.Inputs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: audio:", err)
		return 1
	}
	outs, err := eng.Outputs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: audio:", err)
		return 1
	}

	fmt.Printf("audio devices (capture %d, playback %d):\n", len(ins), len(outs))
	for i, dev := range ins {
		fmt.Printf("  in  %d. %s\n", i, dev.String())
	}
	for i, dev := range outs {
		fmt.Printf("  out %d. %s\n", i, dev.String())
	}
	if len(ins) == 0 || (len(outs) == 0 && loopback) {
		// without a default device there is nothing meaningful to route
		fmt.Fprintln(os.Stderr, "tuiper: audio: need at least one default capture device (and playback, for -loopback)")
		return 1
	}

	// block channel powers the optional loopback (and doubles as the only
	// hand-off from the audio thread; metering reads counters atomically).
	blocks := make(chan []int16, 400)
	dropped := new(atomic.Int64)
	frames := new(atomic.Int64)
	peak := new(atomic.Int64)

	var pStream *audio.Stream
	if loopback {
		pStream, err = eng.OpenPlayback(audio.Options{}, func(n int) []int16 {
			select {
			case b := <-blocks:
				return b
			default:
				return nil // capture not warmed up yet (or too slow): silence
			}
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "tuiper: audio:", err)
			return 1
		}
	}

	cStream, err := eng.OpenCapture(audio.Options{}, func(in []int16) {
		frames.Add(int64(len(in) / 2))
		p := audio.Peak(in)
		for {
			old := peak.Load()
			if int64(p) <= old || peak.CompareAndSwap(old, int64(p)) {
				break
			}
		}
		if !loopback {
			return
		}
		// copy out of the capture buffer for the playback path (the audio
		// thread reuses it right after we return)
		b := make([]int16, len(in))
		copy(b, in)
		select {
		case blocks <- b:
		default:
			dropped.Add(1)
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: audio:", err)
		if pStream != nil {
			pStream.Stop()
		}
		return 1
	}

	mode := "capture"
	if loopback {
		mode = "capture→playback loopback"
	}
	fmt.Printf("%s (48kHz 2ch s16) for %s -- peak so far:\n", mode, d)
	done := make(chan struct{})
	stopMeter := make(chan struct{})
	tick := time.NewTicker(200 * time.Millisecond)
	go func() {
		defer close(done)
		for {
			select {
			case <-tick.C:
				printMeterBar(int16(peak.Load()), frames.Load())
			case <-stopMeter:
				return
			}
		}
	}()

	time.Sleep(d)
	cStream.Stop()
	stopMeter <- struct{}{}
	<-done
	fmt.Println()
	if pStream != nil {
		pStream.Stop()
	}

	f, p := stats(frames, peak)
	fmt.Printf("capture summary: %d frames, peak %.1f dBFS", f, audio.ToDbFS(p))
	if dd := dropped.Load(); dd > 0 {
		fmt.Printf(", %d blocks dropped (playback slower than capture)", dd)
	}
	fmt.Println()
	if f == 0 {
		fmt.Fprintln(os.Stderr, "tuiper: audio: capture produced no data")
		return 1
	}
	return 0
}

func stats(frames, peak *atomic.Int64) (int64, int16) {
	return frames.Load(), int16(peak.Load())
}

func printMeterBar(peak int16, frames int64) {
	const width = 44
	p := peak
	db := audio.ToDbFS(p)
	filled := 0
	if db > -100 {
		filled = int((db + 60) / 60 * width) // -60..0 dB range maps to bar
		if filled > width {
			filled = width
		}
	}
	bar := make([]byte, width)
	for i := range bar {
		if i < filled {
			bar[i] = '#'
		} else {
			bar[i] = '-'
		}
	}
	fmt.Printf("\r  [%s] %6.1f dBFS, %d frames  ", bar, db, frames)
}
