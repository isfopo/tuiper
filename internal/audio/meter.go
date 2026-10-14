// Meter helpers shared by the headless audio check and the TUI panel.
package audio

import "math"

// Peak returns the largest absolute sample magnitude in s.
func Peak(s []int16) int16 {
	var m int16
	for _, v := range s {
		if v < 0 {
			v = -v
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
