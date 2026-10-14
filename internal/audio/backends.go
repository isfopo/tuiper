// Backend selection order for miniaudio, per platform:
//
//   - Windows: WASAPI (shared mode) then legacy DirectSound/WinMM.
//   - Linux: ALSA first — it is what the container's pipewire-alsa rig
//     provides; PulseAudio (pipewire-pulse) and JACK follow as fallbacks.
//   - macOS: CoreAudio.
//   - Other: nil lets miniaudio use its built-in priority.
package audio

import "github.com/gen2brain/malgo"

// backends lists the backends the tuiper dev and release targets use.
// Adding a platform here is enough to make it work unless miniaudio needs
// per-backend config tweaks.
func backends(goos string) []malgo.Backend {
	switch goos {
	case "windows":
		return []malgo.Backend{
			malgo.BackendWasapi,
			malgo.BackendDsound,
			malgo.BackendWinmm,
		}
	case "linux":
		return []malgo.Backend{
			malgo.BackendAlsa,
			malgo.BackendPulseaudio,
			malgo.BackendJack,
		}
	case "darwin":
		return []malgo.Backend{
			malgo.BackendCoreaudio,
		}
	default:
		return nil
	}
}
