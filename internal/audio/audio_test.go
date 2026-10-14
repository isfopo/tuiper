package audio

import (
	"math"
	"runtime"
	"testing"

	"github.com/gen2brain/malgo"
)

func TestPeak(t *testing.T) {
	cases := []struct {
		name string
		in   []int16
		want int16
	}{
		{"empty", nil, 0},
		{"silence", []int16{0, 0, 0}, 0},
		{"positive", []int16{-3, 100, 42}, 100},
		{"negative wins", []int16{10, -200, 9}, 200},
		// regression: negating -32768 without saturation overflowed back to
		// -32768, so a full-scale negative signal read as silence
		{"full-scale negative saturates", []int16{math.MinInt16}, math.MaxInt16},
		{"mixed with min", []int16{1, math.MinInt16, 2}, math.MaxInt16},
	}
	for _, c := range cases {
		if got := Peak(c.in); got != c.want {
			t.Errorf("%s: Peak = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestToDbFS(t *testing.T) {
	if got := ToDbFS(0); got != -100 {
		t.Errorf("ToDbFS(0) = %g, want -100", got)
	}
	if got := ToDbFS(math.MaxInt16); math.Abs(got-(-0.0004)) > 0.01 {
		t.Errorf("ToDbFS(max) = %g, want ~0 dBFS", got)
	}
	// half amplitude = -6.02 dBFS (the -loop check's known test tone level)
	if got := ToDbFS(16000); math.Abs(got-(-6.22)) > 0.02 {
		t.Errorf("ToDbFS(16000) = %g, want ~-6.2 dBFS", got)
	}
	// monotonicity: bigger samples never read quieter
	if ToDbFS(100) >= ToDbFS(1000) {
		t.Error("ToDbFS not monotonic in peak")
	}
}

func TestBackendsPerPlatform(t *testing.T) {
	// explicit first-backend expectations per platform
	firstWant := map[string]malgo.Backend{
		"windows": malgo.BackendWasapi,
		"linux":   malgo.BackendAlsa,
		"darwin":  malgo.BackendCoreaudio,
	}
	for _, c := range []struct {
		goos  string
		count int
	}{
		{"windows", 3},
		{"linux", 3},
		{"darwin", 1},
		{"plan9", 0},
	} {
		bl := backends(c.goos)
		if len(bl) != c.count {
			t.Errorf("backends(%q): got %d, want %d", c.goos, len(bl), c.count)
		}
		if want, ok := firstWant[c.goos]; ok && c.count > 0 && bl[0] != want {
			t.Errorf("backends(%q)[0] = %d, want %d", c.goos, bl[0], want)
		}
	}
	// the dev/release targets always have an explicit backend list
	if backends(runtime.GOOS) == nil {
		t.Errorf("no explicit backends for %s", runtime.GOOS)
	}
}
