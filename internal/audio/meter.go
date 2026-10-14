// Meter helpers shared by the headless audio check and the TUI panel.
package audio

import "math"

// Peak returns the largest absolute sample magnitude in s. A full-scale
// negative sample (-32768) is saturated to 32767 instead of overflowing back
// to a negative value (which would report silence for a maximal signal).
func Peak(s []int16) int16 {
	var m int16
	for _, v := range s {
		if v < 0 {
			v = -v
			if v < 0 { // -32768: negate overflows; saturate
				v = math.MaxInt16
			}
		}
		if v > m {
			m = v
		}
	}
	return m
}

// ToDbFS converts a raw sample magnitude to dB relative to full scale; zero
// samples report -100 dB.
func ToDbFS(peak int16) float64 {
	if peak == 0 {
		return -100
	}
	return 20 * math.Log10(float64(peak)/32768)
}
