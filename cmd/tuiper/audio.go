// Headless audio checks for milestone 2 (no TUI): enumerate devices, meter
// the default capture input for a duration, optionally round trip capture
// into playback (-loopback). runLoop (milestone 3) exercises the looper
// end-to-end: record a synthetic tone round trip, play the loop, verify the
// content level headlessly.
package main

import (
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"time"

	"tuiper/internal/audio"
	"tuiper/internal/looper"
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

// runLoop is the milestone-3 headless check: record d seconds of a
// synthesized test tone round trip (playback → sink monitor → capture, like
// the container rig provides; on real hardware the mic hears the speakers),
// close the take, play the loop back for a moment, then stop the streams and
// verify the recorded loop actually contains signal.
//
// It fails when the take is empty or the loop level is in the noise (-50
// dBFS floor): either means the duplex round trip delivered nothing.
func runLoop(d time.Duration) int {
	eng, err := audio.NewEngine()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: loop:", err)
		return 1
	}
	defer eng.Close()

	ins, err := eng.Inputs()
	if err != nil || len(ins) == 0 {
		fmt.Fprintln(os.Stderr, "tuiper: loop: no capture device")
		return 1
	}

	// capacity must hold the take plus headroom for the playback phase
	l := looper.New(2, 48000, (d + 5*time.Second).Seconds())

	// exact-period test tone: 480 Hz at 48 kHz = a clean 100-frame cycle (~
	// 5 dB below digital full scale so overdubs/mixing never clip)
	tone := make([]int16, 200) // 100 frames × 2 channels
	const amp = 16000
	for f := 0; f < 100; f++ {
		v := int16(float64(amp) * math.Sin(2*math.Pi*float64(f)/100))
		tone[2*f], tone[2*f+1] = v, v
	}
	tonePos := 0

	stream, err := eng.OpenDuplex(audio.Options{}, func(out, in []int16) {
		if out != nil {
			if src := l.Playback(len(out)); src != nil {
				copy(out, src)
			} else {
				for i := range out { // the loop is silent: sound the tone
					out[i] = tone[tonePos]
					tonePos = (tonePos + 1) % len(tone)
				}
			}
		}
		if in != nil {
			l.Capture(in)
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiper: loop:", err)
		return 1
	}

	l.Toggle() // → recording
	fmt.Printf("looper duplex up — recording %.1fs of test tone:\n", d.Seconds())

	done := make(chan struct{})
	stopMeter := make(chan struct{})
	tick := time.NewTicker(200 * time.Millisecond)
	go func() {
		defer close(done)
		for {
			select {
			case <-tick.C:
				p, f := l.TakeMeter()
				printMeterBar(p, f)
			case <-stopMeter:
				return
			}
		}
	}()

	time.Sleep(d)
	l.Toggle() // close the take → playing
	stopMeter <- struct{}{}
	<-done
	fmt.Printf("\nloop: %d frames (%.1fs) — playing it back...\n",
		l.FramesInLoop(), l.Seconds())

	time.Sleep(1500 * time.Millisecond)
	stream.Stop() // quiesce the audio threads before poking looper state

	got := l.Playback(24000) // 0.5s out of the recorded loop
	loopPeak := audio.Peak(got)
	fmt.Printf("loop level: peak %.1f dBFS\n", audio.ToDbFS(loopPeak))
	if l.FramesInLoop() == 0 {
		fmt.Fprintln(os.Stderr, "tuiper: loop: the take is empty")
		return 1
	}
	if audio.ToDbFS(loopPeak) < -50 {
		fmt.Fprintln(os.Stderr,
			"tuiper: loop: round trip delivered silence (check the capture route)")
		return 1
	}
	fmt.Println("looper: round trip OK")
	return 0
}
