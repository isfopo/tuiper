// Package audio wraps tuiper's realtime audio plumbing (device enumeration,
// capture, playback) behind a small Go API over miniaudio, exposed through
// github.com/gen2brain/malgo's CGO bindings. It mirrors the internal/midi
// package: the TUI and future looper stage never inherit any driver details.
//
// Design notes:
//
//   - Frames are interleaved signed 16-bit samples (s16) — the format every
//     supported backend converts to natively and what the looper will record.
//   - An Engine owns one miniaudio context; enumeration and stream creation
//     go through it. Streams are plain per-direction devices (capture-only or
//     playback-only). Full-duplex bridging is the caller's job: a capture
//     stream delivers blocks, a playback stream pulls blocks; a channel
//     between them is an exact, dependency-free round trip.
//   - On Windows this speaks WASAPI (real devices); on Linux it speaks ALSA —
//     where a container exposes the pipewire rig (see scripts/pipewire/), the
//     "default" plugin PCM routes playback into a null sink and capture into
//     that sink's monitor, giving a deterministic loopback.
//
// Callbacks run on the audio thread: they must not block. The package hands
// out fresh slices of captured data, so the consumer is free to keep them.
package audio

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	/*
		#include <stdlib.h>
		#include <string.h>
	*/
	"C"

	"github.com/gen2brain/malgo"
)

// Device is one enumerated audio endpoint.
type Device struct {
	Name      string
	IsDefault bool

	// id is an opaque backend identifier (bytes of a miniaudio device ID).
	// Pass it back in Options.DeviceID to open this device explicitly.
	id []byte
}

// String renders a device for listings: "[default] name".
func (d Device) String() string {
	if d.IsDefault {
		return fmt.Sprintf("[default] %s", d.Name)
	}
	return fmt.Sprintf("[        ] %s", d.Name)
}

// ID returns a copy of the opaque backend identifier to hand back through
// Options.DeviceID. An empty result means "the system default device".
func (d Device) ID() []byte {
	id := make([]byte, len(d.id))
	copy(id, d.id)
	return id
}

// Options configures one stream. Zero values select comfortable defaults.
type Options struct {
	// DeviceID opens a specific endpoint (copy the []byte out of Device).
	// nil uses the system default device.
	DeviceID []byte
	// Channels: 1 or 2 (default 2).
	Channels uint32
	// SampleRate (default 48000).
	SampleRate uint32
	// LatencyMS is the requested callback period (default 10 ms).
	LatencyMS uint32
}

func (o Options) channels() uint32 {
	if o.Channels == 0 {
		return 2
	}
	return o.Channels
}

func (o Options) rate() uint32 {
	if o.SampleRate == 0 {
		return 48000
	}
	return o.SampleRate
}

func (o Options) latencyMS() uint32 {
	if o.LatencyMS == 0 {
		return 10
	}
	return o.LatencyMS
}

// Stream is one running audio device.
type Stream struct {
	dev     *malgo.Device
	stopped sync.Once
}

// Stop halts and uninitializes the device; callbacks stop before it returns.
// Safe to call repeatedly.
func (s *Stream) Stop() {
	if s == nil {
		return
	}
	s.stopped.Do(func() {
		_ = s.dev.Stop()
		s.dev.Uninit()
	})
}

// Engine owns one miniaudio context.
type Engine struct {
	ctx  *malgo.AllocatedContext
	stop sync.Once

	mu   sync.Mutex
	msgs []string // recent backend log lines, kept for error reporting
}

// NewEngine initializes the audio backend (WASAPI on Windows, ALSA first on
// Linux — see backends()). Set TUPIER_AUDIO_DEBUG=1 for backend chatter.
func NewEngine() (*Engine, error) {
	e := &Engine{}

	var logProc malgo.LogProc
	if os.Getenv("TUPIER_AUDIO_DEBUG") != "" {
		logProc = e.note
	}

	ctx, err := malgo.InitContext(backends(runtime.GOOS), malgo.ContextConfig{}, logProc)
	if err != nil {
		return nil, fmt.Errorf("audio context: %w (%s)", err, e.recent())
	}
	e.ctx = ctx
	return e, nil
}

// note stores a backend log line (bounded) for later error context.
func (e *Engine) note(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.msgs) >= 32 {
		e.msgs = e.msgs[1:]
	}
	e.msgs = append(e.msgs, msg)
}

// recent renders the tail of backend log lines (empty state gets a stub).
func (e *Engine) recent() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.msgs) == 0 {
		return "no backend log (set TUPIER_AUDIO_DEBUG=1 for detail)"
	}
	if len(e.msgs) > 4 {
		e.msgs = e.msgs[len(e.msgs)-4:]
	}
	s := e.msgs[0]
	for _, m := range e.msgs[1:] {
		s += " | " + m
	}
	return s
}

// Inputs lists capture (input) devices.
func (e *Engine) Inputs() ([]Device, error) {
	return e.devices(malgo.Capture)
}

// Outputs lists playback (output) devices.
func (e *Engine) Outputs() ([]Device, error) {
	return e.devices(malgo.Playback)
}

func (e *Engine) devices(kind malgo.DeviceType) ([]Device, error) {
	infos, err := e.ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	devs := make([]Device, len(infos))
	for i, info := range infos {
		id := make([]byte, len(info.ID))
		copy(id, info.ID[:])
		devs[i] = Device{
			Name:      info.Name(),
			IsDefault: info.IsDefault != 0,
			id:        id,
		}
	}
	return devs, nil
}

// Close stops open streams of this engine and frees the context. Streams
// previously returned by OpenCapture/OpenPlayback should be considered
// stopped afterwards.
func (e *Engine) Close() {
	e.stop.Do(func() {
		if e.ctx != nil {
			_ = e.ctx.Uninit()
			e.ctx.Free()
			e.ctx = nil
		}
	})
}

func (e *Engine) checkInit() error {
	if e.ctx == nil {
		return fmt.Errorf("audio engine is closed")
	}
	return nil
}

// OpenCapture starts a capture device and delivers interleaved s16 samples
// to deliver. The caller owns the result and stops it when done. deliver is
// called with a fresh slice per capture period; it must not block.
func (e *Engine) OpenCapture(opts Options, deliver func(samples []int16)) (*Stream, error) {
	if err := e.checkInit(); err != nil {
		return nil, err
	}

	cfg, devID, err := deviceConfig(malgo.Capture, opts)
	if err != nil {
		return nil, err
	}
	if devID != nil { // ID must stay alive until InitDevice copied it
		defer C.free(devID)
	}

	dev, err := malgo.InitDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			if input == nil {
				return
			}
			// view the input buffer as interleaved samples (host-endian s16)
			if samples := samplesOf(input); samples != nil {
				deliver(samples)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open capture: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, fmt.Errorf("start capture: %w", err)
	}
	return &Stream{dev: dev}, nil
}

// OpenPlayback starts a playback device and asks pull for every callback's
// worth of samples. pull(n) must return 0..n interleaved samples; a short
// result is padded with silence. pull must not block for long.
func (e *Engine) OpenPlayback(opts Options, pull func(n int) []int16) (*Stream, error) {
	if err := e.checkInit(); err != nil {
		return nil, err
	}

	cfg, devID, err := deviceConfig(malgo.Playback, opts)
	if err != nil {
		return nil, err
	}
	if devID != nil { // ID must stay alive until InitDevice copied it
		defer C.free(devID)
	}

	dev, err := malgo.InitDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			if output == nil {
				return
			}
			out := samplesOf(output)
			if out == nil {
				return
			}
			data := pull(len(out))
			n := copy(out, data)
			if n < len(out) {
				clear(out[n:]) // underrun: silence
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open playback: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, fmt.Errorf("start playback: %w", err)
	}
	return &Stream{dev: dev}, nil
}

// deviceConfig builds a malgo config for one s16 stream. A specific device
// gets its ID copied into C memory (cgo forbids handing over a Go-managed
// buffer; miniaudio copies the ID synchronously inside InitDevice). The C
// pointer is returned to the caller, which must keep it alive across
// malgo.InitDevice and then C.free it — passing a nil cDevID keeps the
// system default.
func deviceConfig(kind malgo.DeviceType, opts Options) (malgo.DeviceConfig, unsafe.Pointer, error) {
	cfg := malgo.DefaultDeviceConfig(kind)
	cfg.SampleRate = opts.rate()
	cfg.PeriodSizeInMilliseconds = opts.latencyMS()
	cfg.Periods = 3

	if kind == malgo.Capture {
		cfg.Capture.Format = malgo.FormatS16
		cfg.Capture.Channels = opts.channels()
	} else {
		cfg.Playback.Format = malgo.FormatS16
		cfg.Playback.Channels = opts.channels()
	}

	if len(opts.DeviceID) == 0 {
		return cfg, nil, nil
	}
	c := C.malloc(C.size_t(len(opts.DeviceID)))
	if c == nil {
		return cfg, nil, fmt.Errorf("alloc device id (%d bytes) failed", len(opts.DeviceID))
	}
	C.memcpy(c, unsafe.Pointer(&opts.DeviceID[0]), C.size_t(len(opts.DeviceID)))
	if kind == malgo.Capture {
		cfg.Capture.DeviceID = c
	} else {
		cfg.Playback.DeviceID = c
	}
	return cfg, c, nil
}

// samplesOf reinterprets an interleaved s16 byte buffer as samples. All
// supported-audio platforms are little-endian; a BE machine would be misread
// (out of scope for tuiper's targets).
func samplesOf(b []byte) []int16 {
	if len(b) < 2 {
		return nil
	}
	return unsafe.Slice((*int16)(unsafe.Pointer(&b[0])), len(b)/2)
}
