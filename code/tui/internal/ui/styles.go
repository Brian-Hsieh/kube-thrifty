package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type Styles struct {
	Title        lipgloss.Style
	Subtle       lipgloss.Style
	Muted        lipgloss.Style
	NodeSelected lipgloss.Style
	NodeNormal   lipgloss.Style
	Error        lipgloss.Style
	BarLabel     lipgloss.Style
	Panel        lipgloss.Style
	AppPanel     lipgloss.Style
}

func NewStyles() Styles {
	return Styles{
		Title:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
		Subtle:       lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		Muted:        lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		NodeSelected: lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")).Padding(0, 1),
		NodeNormal:   lipgloss.NewStyle().Padding(0, 1),
		Error:        lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		BarLabel:     lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
		Panel:        lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1),
		AppPanel:     lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.NoColor{}).Padding(0, 1),
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
