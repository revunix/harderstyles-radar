package tui

import (
	"hardstylesync/internal/event"
	"hardstylesync/internal/i18n"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The characters of the boxes: thin lines with round corners, the title sits in the upper line.
const (
	boxH, boxV   = "─", "│"
	boxTL, boxTR = "╭", "╮"
	boxBL, boxBR = "╰", "╯"
	meterFill    = "■"
	meterEmpty   = "·"
	spinnerChars = "⣾⣽⣻⢿⡿⣟⣯⣷"
	// rowIndent is the room in front of every line of a list: the marked line carries its bar there.
	rowIndent = 2
)

type keyHint struct{ Key, Label string }

func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// padTo cuts or pads a plain or styled text to exactly width cells.
func padTo(s string, width int) string {
	s = fit(s, width)
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

// padStyled puts a text into a field of the given width and colors it. The padding is counted on the
// visible text, so a color does not shift the columns.
func padStyled(style lipgloss.Style, text string, width int) string {
	text = fit(text, width)
	return style.Render(text) + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}

// renderBox draws a box of exactly width x height cells. The title sits in the upper line:
// the name bright, a number behind it ("Releases (120)") muted.
func renderBox(width, height int, title string, frame lipgloss.Style, content []string) []string {
	if width < 4 || height < 2 {
		return nil
	}
	inner := width - 4
	panel := sequenceOf(theme.Panel)

	top := frame.Render(boxTL + boxH)
	if title != "" {
		top += fit(" "+boxTitle(title)+" ", width-4)
	}
	top += frame.Render(strings.Repeat(boxH, max(0, width-ansi.StringWidth(top)-1)) + boxTR)

	lines := []string{top}
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(content) {
			line = fit(content[i], inner)
		}
		middle := " " + line + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + " "
		// The reset only exists to end the panel color. Without color it would be an escape sequence that
		// does nothing, so it is left off.
		if panel != "" {
			middle = withBackground(middle, panel) + "\x1b[0m"
		}
		lines = append(lines, frame.Render(boxV)+middle+frame.Render(boxV))
	}
	return append(lines, frame.Render(boxBL+strings.Repeat(boxH, width-2)+boxBR))
}

func boxTitle(title string) string {
	if open := strings.LastIndex(title, " ("); open > 0 && strings.HasSuffix(title, ")") {
		if _, err := strconv.Atoi(title[open+2 : len(title)-1]); err == nil {
			return theme.Title.Render(title[:open]) + " " + theme.Muted.Render(title[open+2:len(title)-1])
		}
	}
	return theme.Title.Render(title)
}

// renderMeter draws a progress bar: the filled part carries the brand gradient, the rest is dotted.
func renderMeter(width, percent int) string {
	percent = min(max(percent, 0), 100)
	filled := percent * width / 100
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteString(lipgloss.NewStyle().Foreground(gradientAt(float64(i+1) / float64(max(1, width)))).Render(meterFill))
			continue
		}
		b.WriteString(theme.Divider.Render(meterEmpty))
	}
	return b.String()
}

// renderHeader returns the head line and the rule below it: on the left the mark and the name, then the
// path to the current view; on the right the context and the clock.
func renderHeader(width int, mark string, steps []string, status, context, clock string) []string {
	sep := theme.Divider.Render("  │  ")
	head := mark + " " + gradientText("harderstyles") + theme.Muted.Render("-radar")
	if len(steps) > 0 {
		parts := make([]string, len(steps))
		for i, s := range steps {
			parts[i] = theme.Muted.Render(s)
			if i == len(steps)-1 {
				parts[i] = theme.Title.Render(s)
			}
		}
		head += sep + strings.Join(parts, theme.Subtle.Render(" › "))
	}
	if status != "" {
		head += "   " + theme.Warning.Render(status)
	}
	right := theme.Text.Render(clock)
	if context != "" {
		right = theme.Muted.Render(context) + sep + right
	}
	head = fit(head, max(1, width-ansi.StringWidth(right)-2))
	gap := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(right))
	return []string{fit(head+strings.Repeat(" ", gap)+right, width), theme.Divider.Render(strings.Repeat(boxH, width))}
}

// renderFooter renders the key hints (key bright, description muted, a dot between) and wraps them onto lines.
func renderFooter(width int, hints []keyHint) []string {
	gap := theme.Divider.Render("  ·  ")
	var lines []string
	current := ""
	for _, h := range hints {
		piece := theme.Title.Render(h.Key) + " " + theme.Muted.Render(h.Label)
		switch {
		case current == "":
			current = piece
		case ansi.StringWidth(current+gap+piece) > width:
			lines = append(lines, current)
			current = piece
		default:
			current += gap + piece
		}
	}
	return append(lines, current)
}

// detailRow renders one line of the detail column: a fixed width label and the value behind it.
func detailRow(label, value string) string {
	return theme.Muted.Render(padTo(label, 13)) + " " + value
}

// renderRow renders a line of a list. The marked line has a bar in the brand color on the left and a
// background over its whole width.
func renderRow(text string, selected bool, width int) string {
	if !selected {
		return strings.Repeat(" ", rowIndent) + text
	}
	line := text + strings.Repeat(" ", max(0, width-rowIndent-ansi.StringWidth(text)))
	return withBackground(theme.Accent.Render("▌")+" "+theme.SelectedFg.Render(line), sequenceOf(theme.Selected))
}

func statusBadge(s event.Status) string {
	switch s {
	case event.StatusNew:
		return theme.Warning.Render(i18n.T("● neu", "● new"))
	case event.StatusInPlaylist:
		return theme.Muted.Render(i18n.T("✓ drin", "✓ in list"))
	case event.StatusAdded:
		return theme.Success.Render(i18n.T("✓ hinzugefügt", "✓ added"))
	case event.StatusNotFound:
		return theme.Danger.Render(i18n.T("✗ fehlt", "✗ missing"))
	case event.StatusMismatch:
		return theme.Warning.Render(i18n.T("✗ Datum", "✗ date"))
	}
	return theme.Muted.Render(i18n.T("○ offen", "○ open"))
}
