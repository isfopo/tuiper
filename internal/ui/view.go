package ui

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"

	"tuiper/internal/audio"
)

// view styles. Kept deliberately plain: faint the chrome, bright the events.
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginRight(2)

	headingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("242"))

	okStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("245"))

	errStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203"))

	openMarkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205"))
)

// View renders the model. While the devices modal is open the modal library
// composites the (dimmed) base frame with the foreground box, so the return
// value of View() switches content without the renderer noticing a size
// change: both renderings occupy exactly m.height lines.
func (m Model) View() string {
	if m.modal.Opened() {
		return m.modal.View()
	}
	return m.baseView()
}

// baseView is the main frame: title + lamps, the selected I/O summary with
// the capture meter, and the status bar — exactly m.height lines, no
// trailing newline.
func (m Model) baseView() string {
	var b strings.Builder

	// header -----------------------------------------------------------
	if m.width > 0 {
		left := titleStyle.Render("◉ tuiper template") +
			headingStyle.Render("midi + audio io")
		lamps := m.lampView()
		gap := m.width - lipgloss.Width(left) - lipgloss.Width(lamps)
		if gap < 1 {
			gap = 1
		}
		b.WriteString(left + strings.Repeat(" ", gap) + lamps + "\n\n")
	}

	// selected io summary ------------------------------------------------
	write := func(s string) { b.WriteString(s + "\n") }
	write(m.midiLine())
	write(m.audioInLine())
	write(m.audioOutLine())

	// fill the remainder so the status bar stays on the bottom row
	padding := m.height - m.reserved()
	if padding > 0 {
		b.WriteString(strings.Repeat("\n", padding))
	}

	b.WriteString(m.statusBar())
	return b.String()
}

// ioLinesOf counts the summary lines between the header and the status bar.
func ioLinesOf() int {
	return 3 // midi in / audio in / audio out
}

// reserved computes the number of lines the frame occupies besides padding:
// title + blank + summary lines + status bar (2 lines).
func (m Model) reserved() int {
	return 2 + ioLinesOf() + 2
}

// midiLine renders the selected/open midi port.
func (m Model) midiLine() string {
	ap := m.ap
	name := ap.appliedName(secMidi)
	if ap.openNum >= 0 {
		return okStyle.Render("midi in : ") + name + " " + openMarkStyle.Render("●")
	}
	return okStyle.Render("midi in : ") + headingStyle.Render(name)
}

// audioInLine renders the applied capture device with the live meter.
func (m Model) audioInLine() string {
	row := okStyle.Render("audio in: ") + m.ap.inName()
	if m.ap.capturing {
		row += "  " + meterBar(m.ap.dispPeak, m.ap.dispFrames, m.ap.dispDrops)
	}
	return row
}

// audioOutLine renders the applied playback device (and monitor state).
func (m Model) audioOutLine() string {
	row := okStyle.Render("audio out: ") + m.ap.outName()
	if m.ap.monitor {
		row += "  " + headingStyle.Render("(monitor — m toggles)")
	}
	return row
}

// meterBar renders the capture level: a fixed-width bar plus dBFS text; the
// frame rate doubles as proof that capture is actually delivering.
func meterBar(peak int16, framesSince int64, drops uint64) string {
	const width = 20
	db := audio.ToDbFS(peak)
	filled := 0
	if db > -60 {
		filled = int((db + 60) / 60 * width)
		if filled > width {
			filled = width
		}
	}
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	rate := ""
	if framesSince > 0 {
		// knit the window rate into the readout (frames/200ms tick)
		rate = fmt.Sprintf(" %dkf/s", framesSince*5/1000)
	}
	dropTxt := ""
	if drops > 0 {
		dropTxt = " dropped:" + fmt.Sprint(drops)
	}
	return okStyle.Render(fmt.Sprintf("[%s] %6.1fdBFS%s%s", bar, db, rate, dropTxt))
}

// lampView renders the two MIDI activity dots: dark = dark-gray hollow
// circle (idle), lit = bold filled dot in its color for a short window after
// traffic (in green, out magenta). Plain color attributes only — no
// faint/blink escapes, the least supported on Windows terminals.
func (m Model) lampView() string {
	if m.lamps == nil {
		return ""
	}
	dark := lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("○")
	lit := func(stamp *atomic.Int64, onColor string) string {
		if m.lamps.lit(stamp) {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(onColor)).Bold(true).Render("●")
		}
		return dark
	}
	return " " + lit(&m.lamps.in, "42") + " " + lit(&m.lamps.out, "205")
}

func (m Model) statusBar() string {
	var b strings.Builder

	b.WriteString(m.ap.chip() + "  ")
	if m.isErr {
		b.WriteString(errStyle.Render(m.status))
	} else {
		b.WriteString(okStyle.Render(m.status))
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("q quit • d devices • m monitor • r refresh • esc quit • ● midi activity"))

	// NOTE: no trailing newline. The frame must occupy exactly m.height
	// \n-separated rows; a trailing "\n" adds an empty row that overflows
	// the terminal and makes bubbletea's renderer drop rows from the TOP.
	return b.String()
}
