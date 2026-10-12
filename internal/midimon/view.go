package midimon

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

	eventStyle = lipgloss.NewStyle()

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
		sub := headingStyle.Render("midi monitor — v0.1")
		b.WriteString(title + sub + "\n\n")
	}

	// ports --------------------------------------------------------------
	b.WriteString(headingStyle.Render("MIDI inputs (↑/↓ select, ⏎ open/close, r refresh)") + "\n")

	if len(m.ports) == 0 {
		b.WriteString("  " + okStyle.Render("(no input ports — connect your audio interface / pedal)"))
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

	// event log, bottom-anchored -----------------------------------------
	logHeight := m.height - m.reserved()
	if logHeight < 1 {
		logHeight = 1
	}

	b.WriteString("\n" + headingStyle.Render("events (last 512)") + "\n")

	log := m.log
	if len(log) > logHeight {
		log = log[len(log)-logHeight:]
	}
	for _, line := range log {
		b.WriteString(eventStyle.Render(line) + "\n")
	}

	// fill the remainder so the status bar stays on the bottom row
	bodyLines := len(log)
	padding := logHeight - bodyLines
	if padding > 0 {
		b.WriteString(strings.Repeat("\n", padding))
	}

	b.WriteString(m.statusBar())
	return b.String()
}

// reserved computes the number of lines taken by header/ports/status bar.
func (m Model) reserved() int {
	lines := 2 + 1 + len(m.ports) + 2 + 1 // title block + port heading + ports + log heading + log
	return lines
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
	if m.isErr {
		b.WriteString(errStyle.Render(m.status))
	} else {
		b.WriteString(okStyle.Render(m.status))
	}
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("q quit • ↑/k, ↓/j move • enter/space open+close • r refresh • enter toggles"))
	b.WriteString("\n")

	return b.String()
}
