package looper

import (
	"math/rand"
	"testing"
)

// constBlock returns n frames of a constant value on every channel.
func constBlock(ch, frames int, val int16) []int16 {
	b := make([]int16, frames*ch)
	for i := range b {
		b[i] = val
	}
	return b
}

// rampBlock returns n frames counting up by step per frame (every channel
// carries the frame's value), starting at from.
func rampBlock(ch, frames int, from, step int16) []int16 {
	b := make([]int16, frames*ch)
	for f := 0; f < frames; f++ {
		for c := 0; c < ch; c++ {
			b[f*ch+c] = from + step*int16(f)
		}
	}
	return b
}

func at(t *testing.T, b []int16, ch, frame, chanIdx int) int16 {
	t.Helper()
	return b[frame*ch+chanIdx]
}

func TestToggleTransitions(t *testing.T) {
	l := New(2, 48000, 1)
	if l.State() != Idle || l.Seconds() != 0 {
		t.Fatalf("fresh looper: state=%v seconds=%f", l.State(), l.Seconds())
	}

	// empty take falls back to idle
	if s := l.Toggle(); s != Recording {
		t.Fatalf("idle->recording: got %v", s)
	}
	if s := l.Toggle(); s != Idle {
		t.Fatalf("empty take -> idle: got %v", s)
	}
	if l.Seconds() != 0 {
		t.Fatalf("empty take left length: %f", l.Seconds())
	}

	// take closes into playing with its recorded length
	l.Toggle()
	l.Capture(rampBlock(2, 480, 100, 1))
	if n := l.FramesInLoop(); n != 480 {
		t.Fatalf("take length: got %d", n)
	}
	if s := l.Toggle(); s != Playing {
		t.Fatalf("recording->playing: got %v", s)
	}
	if l.FramesInLoop() != 480 || l.Seconds() != 480.0/48000.0 {
		t.Fatalf("closed loop: frames=%d seconds=%f", l.FramesInLoop(), l.Seconds())
	}

	// playing<->overdubbing ping-pong
	if s := l.Toggle(); s != Overdubbing {
		t.Fatalf("playing->overdubbing: got %v", s)
	}
	if s := l.Toggle(); s != Playing {
		t.Fatalf("overdubbing->playing: got %v", s)
	}

	// clear resets to idle and empties the loop
	l.Clear()
	if l.State() != Idle || l.FramesInLoop() != 0 || l.Seconds() != 0 {
		t.Fatalf("clear: state=%v frames=%d", l.State(), l.FramesInLoop())
	}
}

func TestRecordPassthroughSilenceWhileIdleAndRecording(t *testing.T) {
	l := New(1, 100, 1)
	if l.Playback(10) != nil {
		t.Fatal("idle playback must be silent")
	}
	l.Toggle() // recording
	if l.Playback(10) != nil {
		t.Fatal("recording playback must be silent")
	}
}

func TestPlaybackCyclesAndWraps(t *testing.T) {
	const ch, frames = 2, 480
	l := New(ch, 48000, 1)
	l.Toggle()
	l.Capture(rampBlock(ch, frames, 100, 1)) // frame i carries 100+i
	l.Toggle()

	// ask exactly one full loop: content matches, cursor back at 0
	got := l.Playback(frames * ch)
	if len(got) != frames*ch {
		t.Fatalf("playback size %d", len(got))
	}
	for f := 0; f < frames; f++ {
		if want := int16(100 + f); got[f*ch] != want || got[f*ch+1] != want {
			t.Fatalf("frame %d: got %d/%d want %d", f, got[f*ch], got[f*ch+1], want)
		}
	}

	// wrap: 2*frames covers 3 cycles; sampling across boundaries checks it
	for cycle := 0; cycle < 3; cycle++ {
		got = l.Playback(frames * ch)
		for f := 0; f < frames; f++ {
			if want := int16(100 + f); got[f*ch] != want {
				t.Fatalf("cycle %d frame %d: got %d want %d", cycle, f, got[f*ch], want)
			}
		}
	}
	if l.playPos.Load() != 0 {
		t.Fatalf("cursor not re-anchored: %d", l.playPos.Load())
	}

	// odd sample counts: frames advance, strays stay silent
	got = l.Playback(3*ch + 1)
	if len(got) != 3*ch+1 || got[3*ch] != 0 {
		t.Fatalf("stray sample handling: %v", got)
	}
}

func TestOverdubMixesAndSaturates(t *testing.T) {
	const ch, frames = 1, 400
	l := New(ch, 48000, 1)
	l.Toggle()
	l.Capture(constBlock(ch, frames, 30000)) // base: everywhere 30000
	l.Toggle()
	l.Toggle() // overdubbing

	// plain doubling: 30000+30000 = 60000 → clamps to 32767
	l.Capture(constBlock(ch, 100, 30000))
	got := l.Playback(10 * ch)
	if got[0*ch] != 32767 {
		t.Fatalf("no saturation: got %d", got[0*ch])
	}

	// cursor sits at 10 now; add negatives there to check the lower headroom
	l.Capture(constBlock(ch, 100, -20000)) // 32767-20000 = 12767 (frames 10..109)
	if s := l.Toggle(); s != Playing {
		t.Fatalf("overdubbing->playing: got %v", s)
	}
	if p := l.playPos.Load(); p != 10 {
		t.Fatalf("capture must not move the playback cursor: %d", p)
	}
	got = l.Playback(10 * ch) // frames 10..19 hold 12767
	if got[0] != 12767 {
		t.Fatalf("expected 12767 at the mixed region: got %d", got[0])
	}
}

func TestOverdubWrapsLoopBoundary(t *testing.T) {
	const ch, frames = 1, 100
	l := New(ch, 48000, 1)
	l.Toggle()
	l.Capture(rampBlock(ch, frames, 0, 1)) // frame i = i (0..99)
	l.Toggle()

	// advance the cursor to frame 90, then overdub 20 frames: the mix
	// straddles the wrap (90..99 then 0..9)
	if got := l.Playback(90 * ch); len(got) == 0 {
		t.Fatal("warmup playback failed")
	}
	l.Toggle() // overdubbing
	l.Capture(rampBlock(ch, 20, 0, 10))
	l.Toggle()

	got := l.Playback(30 * ch) // frames 90..99 then 0..19, base beyond
	for f := 0; f < 30; f++ {
		idx := 90 + f
		if idx >= frames {
			idx -= frames // wrapped
		}
		want := int16(idx) // base content
		if f < 20 {        // only the overdubbed straddle is modified
			want += int16(f) * 10
		}
		if got[f] != want {
			t.Fatalf("frame %d: got %d want %d", f, got[f], want)
		}
	}
}

func TestRecordAutoClosesAtCapacity(t *testing.T) {
	const ch = 1
	l := New(ch, 1000, 0.5) // maxFrames = 500
	l.Toggle()
	l.Capture(rampBlock(ch, 600, 7, 0)) // 600 frames into a 500-frame buffer
	if s := l.State(); s != Playing {
		t.Fatalf("take must auto-close at capacity: %v", s)
	}
	if l.FramesInLoop() != 500 {
		t.Fatalf("loop length capped: %d", l.FramesInLoop())
	}
	got := l.Playback(510 * ch) // wraps past the end
	for f := 0; f < 500; f++ {
		if got[f] != 7 {
			t.Fatalf("frame %d: got %d", f, got[f])
		}
	}
}

func TestClearClearsContent(t *testing.T) {
	const ch, frames = 1, 100
	l := New(ch, 48000, 1)
	l.Toggle()
	l.Capture(constBlock(ch, frames, 12345))
	l.Toggle()
	l.Clear()

	l.Toggle() // fresh take: buffer must start from silence
	if got := l.Playback(5 * ch); got != nil {
		t.Fatal("recording playback silent")
	}
	l.Capture(constBlock(ch, 10, 7)) // mark: silence everywhere else
	l.Capture(constBlock(ch, frames-10, 0))
	l.Toggle()
	got := l.Playback(frames * ch)
	for f := 10; f < frames; f++ {
		if got[f] != 0 { // a stale buffer would still hold 12345 here
			t.Fatalf("stale content after clear at frame %d: %d", f, got[f])
		}
	}
}

func TestMeterPassthrough(t *testing.T) {
	l := New(1, 48000, 1)
	if _, n := l.TakeMeter(); n != 0 {
		t.Fatalf("empty window: %d", n)
	}
	l.Capture(constBlock(1, 40, -200))
	l.Capture(constBlock(1, 10, 300))
	peak, frames := l.TakeMeter()
	if peak != 300 || frames != 50 {
		t.Fatalf("meter: peak=%d frames=%d", peak, frames)
	}
	if peak, frames := l.TakeMeter(); peak != 0 || frames != 0 {
		t.Fatalf("window not drained: peak=%d frames=%d", peak, frames)
	}
	l.Capture(constBlock(1, 1, -32768)) // abs of min-int16 must saturate, not flip
	if peak, _ := l.TakeMeter(); peak != 32767 {
		t.Fatalf("min int16 abs: peak=%d", peak)
	}
}

func TestRandomPlaybackNeverPanics(t *testing.T) {
	l := New(2, 480, 1)
	rnd := rand.New(rand.NewSource(1))
	l.Toggle()
	l.Capture(rampBlock(2, 480, 1, 1))
	l.Toggle()
	for i := 0; i < 200; i++ {
		_ = l.Playback(1 + rnd.Intn(1000))
		if i%7 == 0 {
			l.Capture(rampBlock(2, 1+rnd.Intn(50), 1, 1)) // meters only while playing
		}
	}
}
