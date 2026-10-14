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
			Faint(true)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	openMarkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	errStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	okStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("250"))

	helpStyle = lipgloss.NewStyle().
			Faint(true)
)

func (m Model) View() string {
	var b strings.Builder

	// header -----------------------------------------------------------
	if m.width > 0 {
		title := titleStyle.Render("◉ tuiper")
		sub := headingStyle.Render("midi + audio monitor")
		left := title + sub
		lamps := m.lampView()
		gap := m.width - lipgloss.Width(left) - lipgloss.Width(lamps)
		if gap < 1 {
			gap = 1
		}
		b.WriteString(left + strings.Repeat(" ", gap) + lamps + "\n\n")
	}

	// ports --------------------------------------------------------------
	b.WriteString(headingStyle.Render("midi (↑/↓ select, ⏎ open/close, r refresh)") + "\n")

	if len(m.ports) == 0 {
		b.WriteString("  " + okStyle.Render("(no input ports — connect your audio interface / pedal)") + "\n")
	} else {
		for i, p := range m.ports {
			cursor := "  "
			name := p
			if i == m.cursor {
				cursor = "> "
				name = selectedStyle.Render(p)
			}
			state := "  "
			if i == m.openNum {
				state = openMarkStyle.Render("•")
			}
			b.WriteString("  " + cursor + name + " " + state + "\n")
		}
	}

	// audio panel ---------------------------------------------------------
	m.audioView(&b)

	// fill the remainder so the status bar stays on the bottom row
	padding := m.height - m.reserved()
	if padding > 0 {
		b.WriteString(strings.Repeat("\n", padding))
	}

	b.WriteString(m.statusBar())
	return b.String()
}

// audioLinesOf counts the lines the audio panel occupies, matching exactly
// what audioView renders — reserved must never drift from the drawn output
// or the status bar slides around.
func audioLinesOf(m Model) int {
	if m.ap == nil {
		return 0
	}
	if m.ap.eng == nil {
		return 2 // heading + error line
	}
	n := 1 + 3 // heading + in/out/chan rows
	if m.ap.focus != dropNone {
		n += m.ap.dropdownCount() + 1 // options + controls hint
	}
	return n
}

// audioView renders the audio panel into b. Rows for device selection are
// the dropdowns proper: pressing the key in front of a row expands the
// option list right below it.
func (m Model) audioView(b *strings.Builder) {
	if m.ap == nil {
		return
	}
	write := func(s string) { b.WriteString(s + "\n") }

	heading := "audio"
	if m.ap.eng == nil {
		write(headingStyle.Render(heading))
		write("  " + errStyle.Render("(unavailable: "+m.ap.initErr.Error()+")"))
		return
	}

	write(headingStyle.Render(heading + " (i in • o out • c channels • m monitor)"))

	// in row: selection + live meter
	inRow := "  in : " + m.ap.inName() + "  " + meterBar(m.ap.dispPeak, m.ap.dispFrames, m.ap.dispDrops)
	write(m.markRow(dropIn, inRow, m.ap.capturing))

	outRow := "  out: " + m.ap.outName()
	if m.ap.monitor {
		outRow += "   [monitor → on]"
	}
	write(m.markRow(dropOut, outRow, m.ap.playStream != nil))

	chanName := map[int]string{chanStereo: "stereo (2)", chanMono: "mono (1)"}[m.ap.chans]
	write(m.markRow(dropChan, "  chan: "+chanName, false))

	// expanded dropdown: the option list of the focused row
	switch m.ap.focus {
	case dropIn:
		m.dropdownView(b, m.ap.inOptions(), m.ap.inSel)
	case dropOut:
		m.dropdownView(b, m.ap.outOptions(), m.ap.outSel)
	case dropChan:
		opts := make([]string, len(channelCounts))
		for i, c := range channelCounts {
			opts[i] = fmt.Sprintf("%d channels", c)
		}
		m.dropdownViewStrings(b, opts, m.ap.cursor)
	default:
		return
	}
	write(helpStyle.Render("  (↑/↓ j/k move • ⏎ select • esc cancel)"))
}

// markRow renders an audio row with its dropdown affordance and activity
// mark. It highlights the row whose dropdown is currently open.
func (m Model) markRow(dropdown int, row string, active bool) string {
	name := row
	if m.ap.focus == dropdown {
		name = "  > " + selectedStyle.Render(strings.TrimPrefix(row, "  "))
	} else if active {
		name += " " + openMarkStyle.Render("•")
	}
	return name
}

// dropdownView renders the options of a device dropdown, cursor included.
func (m Model) dropdownView(b *strings.Builder, opts []audio.Device, sel int) {
	strs := make([]string, len(opts))
	for i, o := range opts {
		marker := ""
		if i == sel {
			marker = "[current] "
		}
		strs[i] = marker + o.String()
	}
	m.dropdownViewStrings(b, strs, m.ap.cursor)
}

func (m Model) dropdownViewStrings(b *strings.Builder, opts []string, cursor int) {
	for i, o := range opts {
		if i == cursor {
			b.WriteString("    > " + selectedStyle.Render(o) + "\n")
			continue
		}
		b.WriteString("      " + o + "\n")
	}
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
		rate = fmt.Sprintf(" %4.1fkf/s", float64(framesSince)/1000/0.2)
	}
	dropTxt := ""
	if drops > 0 {
		dropTxt = errStyle.Render(fmt.Sprintf(" drops=%d", drops))
	}
	return okStyle.Render(fmt.Sprintf("[%s] %6.1fdBFS%s%s", bar, db, rate, dropTxt))
}

// lampView renders the two MIDI activity dots, right-aligned by the caller:
// dark = dark-gray hollow circle (idle), lit = bold filled dot in its color
// for a short window after traffic (in green, out magenta). Plain color
// attributes only — no faint/blink escapes, the least supported on Windows
// consoles.
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

// reserved computes the number of lines occupied above the status bar:
// title block + midi heading + ports + audio panel + status bar (2 lines).
func (m Model) reserved() int {
	return 2 + 1 + len(m.ports) + audioLinesOf(m) + 2
}

func (m Model) statusBar() string {
	var b strings.Builder

	if m.monitor != nil {
		state, stateStyle := "idle", "250"
		if m.monitor.Active() {
			state = "live"
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(stateStyle)).Bold(state == "live")
		b.WriteString(style.Render("["+state+"]") + " ")
	}

	b.WriteString(okStyle.Render(fmt.Sprintf("events=%d clocks=%d dropped=%d", m.events, m.clocks, m.dropped)))
	b.WriteString("  ")
	if m.ap != nil {
		b.WriteString(m.ap.chip())
	}
	b.WriteString("  ")
	if m.isErr {
		b.WriteString(errStyle.Render(m.status))
	} else {
		b.WriteString(okStyle.Render(m.status))
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("q quit • ↑/k, ↓/j move • enter/space open/select • i,o,c audio dropdowns • m monitor • r refresh • esc cancels dropdown • ● midi in/out lamps top right"))
	b.WriteString("\n")

	return b.String()
}
