package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type Styles struct {
	Title        lipgloss.Style
	Popup        lipgloss.Style
	Subtle       lipgloss.Style
	Muted        lipgloss.Style
	NodeSelected lipgloss.Style
	NodeNormal   lipgloss.Style
	Warn         lipgloss.Style
	Error        lipgloss.Style
	BarLabel     lipgloss.Style
	Panel        lipgloss.Style
	AppPanel     lipgloss.Style
}

func NewStyles() Styles {
	return Styles{
		Title:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00ff00")),
		Popup:        lipgloss.NewStyle().Foreground(lipgloss.Color("#ff82c0")), // TODO:
		Subtle:       lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a")),
		Muted:        lipgloss.NewStyle().Foreground(lipgloss.Color("#585858")),
		NodeSelected: lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffd7")).Background(lipgloss.Color("#005f87")).Padding(0, 1),
		NodeNormal:   lipgloss.NewStyle().Padding(0, 1),
		Warn:         lipgloss.NewStyle().Foreground(lipgloss.Color("#ffbf00")),
		Error:        lipgloss.NewStyle().Foreground(lipgloss.Color("#ff3b30")),
		BarLabel:     lipgloss.NewStyle().Foreground(lipgloss.Color("#bcbcbc")),
		Panel:        lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#44444")).Padding(0, 1),
		AppPanel:     lipgloss.NewStyle().Padding(1),
	}
}

func ProgressBar(value float64, width int) string {
	width -= 2 // 2 braces
	filled := int(math.Round(float64(width) * value))
	filled = max(0, min(filled, width))
	return fmt.Sprintf("[%s%s]", strings.Repeat("=", filled), strings.Repeat("-", width-filled))
}

func EmptyProgressBar(width int) string {
	width -= 2 // 2 braces
	return fmt.Sprintf("[%s]", strings.Repeat(" ", width))
}
