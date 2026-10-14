// Package looper implements tuiper's loop engine: a preallocated interleaved
// s16 buffer that records, overdubs and plays back cyclically under a tiny
// state machine (idle → recording → playing → overdubbing → playing …).
//
// The package is deliberately audio-API-agnostic: the TUI (or a headless
// harness) attaches it to real streams and feeds two callbacks:
//
//	Capture(block)       — one capture period, called on the audio thread
//	Playback(n) []int16  — one playback period, called on the audio thread
//
// Everything else is buffer math, so the state machine and the wrap/dub
// arithmetic are unit-testable without any audio backend.
//
// Threading rules:
//
//   - Capture/Playback run on audio threads: they touch only the
//     preallocated buffer and atomics — no locks, no allocation (Playback
//     reuses a playback-thread scratch buffer), no blocking.
//   - Command methods (Toggle/Clear, Seconds) are called from the UI
//     goroutine, which is the only source of state transitions.
//   - The capture clock is authoritative while recording; the playback
//     cursor advances per playback callback. Exact cross-device sample
//     alignment is deferred to the clock-sync milestone.
package looper

import "sync/atomic"

// State is one node of the looper state machine.
type State int32

const (
	Idle        State = iota // silent, loop empty/kept
	Recording                // first take: capture fills the buffer, playback silent
	Playing                  // loop cycles through playback
	Overdubbing              // loop cycles AND capture mixes on top of it
)

// String renders the state for the UI/status line.
func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Recording:
		return "rec"
	case Playing:
		return "play"
	case Overdubbing:
		return "overdub"
	}
	return "?"
}

// Looper is the loop engine. Create with New; attach Capture/Playback to
// capture/playback stream callbacks; drive transitions with Toggle/Clear.
type Looper struct {
	ch        int     // interleaved channels (1 or 2)
	rate      uint32  // sample rate, for frame↔time conversions
	maxFrames int     // buffer capacity in frames
	buf       []int16 // loop storage, frame-interleaved, beat into zeroes

	state   atomic.Int32 // a State
	recN    atomic.Int64 // frames written so far in the current/last take
	length  atomic.Int64 // loop length in frames (0 until the take closes)
	playPos atomic.Int64 // next frame playback reads (valid while looping)

	winPeak   atomic.Int64 // meter passthrough: max |sample| this window
	winFrames atomic.Int64 // meter passthrough: frames this window

	playBuf []int16 // playback-thread scratch (reallocated only if too small)
}

// New returns a looper with capacity for maxSeconds of recording. sampleRate
// and channels describe the attached streams' format (s16, interleaved).
func New(channels int, sampleRate uint32, maxSeconds float64) *Looper {
	if channels < 1 {
		channels = 1
	}
	maxFrames := int(float64(sampleRate) * maxSeconds)
	if maxFrames < 1 {
		maxFrames = 1
	}
	return &Looper{
		ch:        channels,
		rate:      sampleRate,
		maxFrames: maxFrames,
		buf:       make([]int16, maxFrames*channels),
	}
}

// State reports the current state.
func (l *Looper) State() State { return State(l.state.Load()) }

// FramesInLoop returns the closed loop's length, or the take length while
// recording (0 while idle). Useful for UI readouts.
func (l *Looper) FramesInLoop() int64 {
	if State(l.state.Load()) == Recording {
		return l.recN.Load()
	}
	return l.length.Load()
}

// Seconds converts the current take/loop to seconds (0 when empty).
func (l *Looper) Seconds() float64 {
	return float64(l.FramesInLoop()) / float64(l.rate)
}

// TakeMeter drains the meter passthrough: peak |sample| and frame count
// since the previous call. The peak saturates at 32767 (mirroring the panel
// meter; -32768 is treated as full scale). Model the audio panel's meter
// reads on this.
func (l *Looper) TakeMeter() (peak int16, frames int64) {
	p := l.winPeak.Swap(0)
	if p > 32767 {
		p = 32767
	}
	return int16(p), l.winFrames.Swap(0)
}

// Toggle advances the state machine and returns the resulting state:
//
//	idle → recording (fresh take: take/loop/pos reset)
//	recording → playing (take closed; empty takes fall back to idle)
//	playing → overdubbing
//	overdubbing → playing
//
// Only the UI goroutine calls this, exactly one transition is attempted, and
// a CAS guards the write; a lost race (impossible under that discipline)
// leaves the state unchanged.
func (l *Looper) Toggle() State {
	switch l.State() {
	case Idle:
		l.recN.Store(0)
		l.playPos.Store(0)
		l.length.Store(0)
		l.state.CompareAndSwap(int32(Idle), int32(Recording))

	case Recording:
		n := l.recN.Load()
		if n < 1 {
			l.state.CompareAndSwap(int32(Recording), int32(Idle))
			break
		}
		l.length.Store(n)
		l.playPos.Store(0)
		l.state.CompareAndSwap(int32(Recording), int32(Playing))

	case Playing:
		l.state.CompareAndSwap(int32(Playing), int32(Overdubbing))

	case Overdubbing:
		l.state.CompareAndSwap(int32(Overdubbing), int32(Playing))
	}
	return l.State()
}

// Clear stops everything and empties the loop (back to idle). Audio threads
// may still be inside one last callback holding the old state; at worst a
// torn sample lands in the idle buffer, and the next take overwrites
// irrevocably — no lock needed for that.
func (l *Looper) Clear() {
	l.state.Store(int32(Idle))
	l.recN.Store(0)
	l.length.Store(0)
	l.playPos.Store(0)
	clear(l.buf)
}

// Capture consumes one capture block (interleaved s16, fresh slice owned by
// the caller): feeds the meter, and in recording writes through into the
// buffer, in overdubbing mixes on top of the loop at the playback cursor.
func (l *Looper) Capture(b []int16) {
	l.meter(b)
	switch l.State() {
	case Recording:
		l.recordWrite(b)
	case Overdubbing:
		l.overdubWrite(b)
	}
}

// Playback fills up to n interleaved samples from the loop, advancing the
// cursor cyclically, and returns them; when nothing should sound (idle,
// recording, no take) it returns nil. The audio engine pads short results
// with silence. The result aliases an internal scratch buffer: the caller
// must copy it out before the next playback callback.
func (l *Looper) Playback(n int) []int16 {
	if n <= 0 {
		return nil
	}
	switch l.State() {
	case Playing, Overdubbing:
	default:
		return nil
	}
	length := int(l.length.Load())
	if length <= 0 {
		return nil
	}

	if cap(l.playBuf) < n {
		l.playBuf = make([]int16, n)
	}
	out := l.playBuf[:n]
	buf := l.buf
	ch := l.ch

	frames := n / ch // trailing strays (n%ch) stay silent
	pos := int(l.playPos.Load())
	for written := 0; written < frames; {
		// copy the largest contiguous run of frames up to the wrap point
		srcFrame := pos % length
		take := length - srcFrame
		if take > frames-written {
			take = frames - written
		}
		copy(out[written*ch:(written+take)*ch], buf[srcFrame*ch:(srcFrame+take)*ch])
		written += take
		pos += take
	}
	l.playPos.Store(int64(pos % length))
	// the frame region was copied flush; the odd-sample tail must be silent,
	// not stale scratch from a previous callback
	clear(out[frames*ch:])
	return out
}

// ---- audio-thread helpers ---------------------------------------------

// meter folds a block into the peak/window atomics (int32 arithmetic: the
// negation of -32768 does not exist in int16).
func (l *Looper) meter(b []int16) {
	l.winFrames.Add(int64(len(b) / l.ch))
	for _, s := range b {
		a := int32(s)
		if a < 0 {
			a = -a
		}
		for {
			old := l.winPeak.Load()
			if int64(a) <= old || l.winPeak.CompareAndSwap(old, int64(a)) {
				break
			}
		}
	}
}

// recordWrite appends a block through into the buffer during the first
// take. A take that runs into the capacity ceiling closes automatically at
// exactly maxFrames and starts playing (excess tail samples dropped).
func (l *Looper) recordWrite(b []int16) {
	ch := l.ch
	n := int(l.recN.Load())
	room := l.maxFrames - n
	if room <= 0 {
		l.closeTakeAtMax()
		return
	}
	if len(b) > room*ch {
		b = b[:room*ch]
	}
	copy(l.buf[n*ch:], b)
	n += len(b) / ch
	l.recN.Store(int64(n))
	if n >= l.maxFrames {
		l.closeTakeAtMax()
	}
}

// closeTakeAtMax ends the take at exactly the capacity ceiling.
func (l *Looper) closeTakeAtMax() {
	l.recN.Store(int64(l.maxFrames))
	l.length.Store(int64(l.maxFrames))
	l.playPos.Store(0)
	l.state.CompareAndSwap(int32(Recording), int32(Playing))
}

// overdubWrite mixes a capture block on top of the loop at the playback
// cursor, saturating instead of wrapping. Playback advances the same cursor
// concurrently; both sides only touch their own arithmetic, and a sample
// torn in the overlap region is negligible.
func (l *Looper) overdubWrite(b []int16) {
	length := int(l.length.Load())
	if length <= 0 {
		return
	}
	ch := l.ch
	buf := l.buf
	pos := int(l.playPos.Load()) % length
	for f := 0; f < len(b)/ch; f++ {
		i := ((pos + f) % length) * ch
		for c := 0; c < ch; c++ {
			sum := int(buf[i+c]) + int(b[f*ch+c])
			switch {
			case sum > 32767:
				buf[i+c] = 32767
			case sum < -32768:
				buf[i+c] = -32768
			default:
				buf[i+c] = int16(sum)
			}
		}
	}
}
