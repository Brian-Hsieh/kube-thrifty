package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"kube-thrifty/tui/internal/api"
	"kube-thrifty/tui/internal/kube"
	"kube-thrifty/tui/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeSortPrompt
	modeFilterPrompt
	modeHelp
)

type sortMode int

const (
	sortNone sortMode = iota
	sortByName
	sortByUsage
	sortByLimit
)

type resourceMode int

const (
	resourceMemory resourceMode = iota
	resourceCPU
)

type fetchResultMsg struct {
	nodes     []api.Node
	fetchedAt time.Time
	err       error
}

type tickMsg struct{}

type model struct {
	forwarder        *kube.Forwarder
	interval         time.Duration
	styles           ui.Styles
	nodes            []api.Node
	visibleNodes     []api.Node
	selected         int
	selectedName     string
	updating         bool
	lastUpdated      time.Time
	lastErr          string
	mode             inputMode
	sortBy           sortMode
	filterInput      string
	activeFilter     string
	filterBeforeEdit string
	resource         resourceMode
	width            int
	height           int
}

func NewModel(forwarder *kube.Forwarder, interval time.Duration) tea.Model {
	return model{
		forwarder: forwarder,
		interval:  interval,
		styles:    ui.NewStyles(),
		selected:  0,
		updating:  true,
		mode:      modeNormal,
		sortBy:    sortNone,
		resource:  resourceMemory,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tickMsg:
		if m.updating {
			return m, m.tickCmd()
		}
		m.updating = true
		return m, tea.Batch(m.fetchCmd(), m.tickCmd())
	case fetchResultMsg:
		m.updating = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}

		m.nodes = msg.nodes
		m.lastUpdated = msg.fetchedAt
		m.lastErr = ""
		m.recomputeVisibleNodes()
		return m, nil
	default:
		return m, nil
	}
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeNormal:
		switch msg.String() {
		case "q", "ctrl+c":
			m.forwarder.Stop()
			return m, tea.Quit
		case "j", "down":
			if len(m.visibleNodes) > 0 {
				m.selected = min(m.selected+1, len(m.visibleNodes)-1)
				m.selectedName = m.visibleNodes[m.selected].Name
			}
		case "k", "up":
			if len(m.visibleNodes) > 0 {
				m.selected = max(m.selected-1, 0)
				m.selectedName = m.visibleNodes[m.selected].Name
			}
		case "s":
			m.mode = modeSortPrompt
		case "/":
			m.mode = modeFilterPrompt
			m.filterBeforeEdit = m.activeFilter
			m.filterInput = m.activeFilter
		case "m":
			m.resource = resourceMemory
		case "c":
			m.resource = resourceCPU
		case "?":
			m.mode = modeHelp
		}
		return m, nil
	case modeSortPrompt:
		switch strings.ToLower(msg.String()) {
		case "esc":
			m.mode = modeNormal
		case "n":
			m.sortBy = sortByName
			m.mode = modeNormal
			m.recomputeVisibleNodes()
		case "u":
			m.sortBy = sortByUsage
			m.mode = modeNormal
			m.recomputeVisibleNodes()
		case "l":
			m.sortBy = sortByLimit
			m.mode = modeNormal
			m.recomputeVisibleNodes()
		}
		return m, nil
	case modeFilterPrompt:
		switch msg.String() {
		case "esc":
			m.activeFilter = m.filterBeforeEdit
			m.filterInput = m.filterBeforeEdit
			m.mode = modeNormal
			m.recomputeVisibleNodes()
		case "enter":
			m.activeFilter = strings.TrimSpace(m.filterInput)
			m.mode = modeNormal
			m.recomputeVisibleNodes()
		case "backspace", "ctrl+h":
			m.filterInput = trimLastRune(m.filterInput)
			m.activeFilter = strings.TrimSpace(m.filterInput)
			m.recomputeVisibleNodes()
		default:
			if msg.Type == tea.KeyRunes {
				m.filterInput += string(msg.Runes)
				m.activeFilter = strings.TrimSpace(m.filterInput)
				m.recomputeVisibleNodes()
			}
		}
		return m, nil
	case modeHelp:
		switch msg.String() {
		case "esc", "?":
			m.mode = modeNormal
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading..."
	}

	header := m.styles.Title.Render("Kube-Thrifty") + "  " + m.statusLine()
	menu := m.styles.Muted.Render("j/k navigate  q quit  s sort  / filter  c cpu  m memory  ? help")

	inputLine := m.renderInputBuffer()

	leftWidth := max(26, m.width/4)
	rightWidth := max(40, m.width-leftWidth-3)

	nodePanel := m.styles.Panel.Width(leftWidth - 2).Render(m.renderNodeList(leftWidth - 4))
	detailPanel := m.styles.Panel.Width(rightWidth - 2).Render(m.renderDetails(rightWidth - 4))
	body := lipgloss.JoinHorizontal(lipgloss.Top, nodePanel, detailPanel)

	return lipgloss.JoinVertical(lipgloss.Left, header, menu, "", inputLine, body)
}

func (m model) statusLine() string {
	parts := []string{}
	parts = append(parts, m.styles.Subtle.Render(fmt.Sprintf("target: %s", m.forwarder.ActiveTarget())))

	if m.updating {
		parts = append(parts, m.styles.Subtle.Render("updating"))
	}

	if !m.lastUpdated.IsZero() {
		parts = append(parts, m.styles.Muted.Render("last: "+m.lastUpdated.Format("15:04:05")))
	}

	if m.lastErr != "" {
		errText := m.lastErr
		if len(errText) > 60 {
			errText = errText[:60] + "..."
		}
		parts = append(parts, m.styles.Error.Render("retrying: "+errText))
	}

	return strings.Join(parts, "  |  ")
}

func (m model) renderNodeList(width int) string {
	if len(m.visibleNodes) == 0 {
		if m.activeFilter != "" {
			return m.styles.Error.Render("No nodes match current filter")
		}
		if m.lastErr != "" {
			return m.styles.Error.Render("No data yet. Retrying every 10s.")
		}
		return m.styles.Subtle.Render("Waiting for metrics data...")
	}

	lines := make([]string, 0, len(m.visibleNodes)+1)
	lines = append(lines, m.styles.Subtle.Render("Nodes"))
	for i, node := range m.visibleNodes {
		line := node.Name
		if len(line) > width && width > 3 {
			line = line[:width-3] + "..."
		}
		if i == m.selected {
			lines = append(lines, m.styles.NodeSelected.Render(line))
			continue
		}
		lines = append(lines, m.styles.NodeNormal.Render(line))
	}

	return strings.Join(lines, "\n")
}

func (m model) renderDetails(width int) string {
	if len(m.visibleNodes) == 0 {
		return m.styles.Subtle.Render("No node selected")
	}

	var lines []string
	node := m.visibleNodes[m.selected]
	title := fmt.Sprintf("%%s on Node: %s", node.Name)

	labelWidth := 32
	barWidth := max(10, min(20, (width-labelWidth)*3/5))
	utilWidth := barWidth + 7
	formatter := "%-*s %-*s %s"
	legend := func(valueHeader string) string {
		return fmt.Sprintf(formatter, labelWidth, "ns/pod:container", utilWidth, "Utilization", valueHeader)
	}

	if m.resource == resourceCPU {
		containers := sortCPUContainers(node.Containers, m.sortBy)
		if len(containers) == 0 {
			return m.styles.Subtle.Render("No container cpu data on selected node")
		}

		lines = append(lines, m.styles.Subtle.Render(fmt.Sprintf(title, "CPU")), "")
		lines = append(lines, m.styles.Subtle.Render(legend("Rate(mCPU)")), "")

		for _, c := range containers {
			label := fmt.Sprintf("%s/%s:%s", c.Namespace, c.PodName, c.Name)
			if len(label) > labelWidth {
				label = label[:labelWidth-3] + "..."
			}

			valueText := "<1"
			if c.CPURate > 0 {
				valueText = fmt.Sprintf("%.0f", c.CPURate)
			}

			var util string
			if c.CPUUtilization != -1 {
				bar := ui.ProgressBar(c.CPUUtilization, barWidth)
				util = fmt.Sprintf("%-*s", utilWidth,
					fmt.Sprintf("%s %s", bar, fmt.Sprintf("%.1f%%", c.CPUUtilization*100)))
			} else {
				util = fmt.Sprintf("%-*s", utilWidth, "N/A")
			}

			line := fmt.Sprintf(formatter, labelWidth, label, utilWidth, util, valueText)
			lines = append(lines, m.styles.BarLabel.Render(line))
		}

		return strings.Join(lines, "\n")
	}

	containers := sortMemoryContainers(node.Containers, m.sortBy)
	if len(containers) == 0 {
		return m.styles.Subtle.Render("No container memory data on selected node")
	}

	lines = append(lines, m.styles.Subtle.Render(fmt.Sprintf(title, "Memory")), "")
	lines = append(lines, m.styles.Subtle.Render(legend("WSS(MB)")), "")

	for _, c := range containers {
		label := fmt.Sprintf("%s/%s:%s", c.Namespace, c.PodName, c.Name)
		if len(label) > labelWidth {
			label = label[:labelWidth-3] + "..."
		}

		memMB := bytesToMB(c.MemWorkingSet)
		valueText := fmt.Sprintf("%.1f", memMB)

		var util string
		if c.MemUtilization != -1 {
			bar := ui.ProgressBar(c.MemUtilization, barWidth)
			util = fmt.Sprintf("%-*s", utilWidth, fmt.Sprintf("%s %s", bar, fmt.Sprintf("%.1f%%", c.MemUtilization*100)))
		} else {
			util = fmt.Sprintf("%-*s", utilWidth, "N/A")
		}

		line := fmt.Sprintf(formatter, labelWidth, label, utilWidth, util, valueText)
		lines = append(lines, m.styles.BarLabel.Render(line))
	}

	return strings.Join(lines, "\n")
}

func (m model) renderInputBuffer() string {
	switch m.mode {
	case modeSortPrompt:
		return m.styles.Subtle.Render("Sort by [n]ame, [u]sage, [l]imit")
	case modeFilterPrompt:
		return m.styles.Subtle.Render(fmt.Sprintf("Filter (Namespace/Pod):%s", m.filterInput))
	case modeHelp:
		return m.styles.Subtle.Render("Help: j/k move  s sort  / filter(live)  c cpu  m memory  Enter apply  Esc cancel/close  q quit")
	default:
		parts := []string{}
		parts = append(parts, "view: "+m.resourceLabel())
		if m.sortBy != sortNone {
			parts = append(parts, "sort: "+m.sortLabel())
		}
		if m.activeFilter != "" {
			parts = append(parts, "filter: "+m.activeFilter)
		}
		if len(parts) == 0 {
			return m.styles.Muted.Render("")
		}
		return m.styles.Muted.Render(strings.Join(parts, "  |  "))
	}
}

func (m model) fetchCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()

		if err := m.forwarder.EnsureRunning(); err != nil {
			return fetchResultMsg{err: fmt.Errorf("port-forward unavailable: %w", err)}
		}

		nodes, err := api.FetchMemory(ctx, m.forwarder.LocalPort())
		if err != nil {
			return fetchResultMsg{err: err}
		}

		return fetchResultMsg{nodes: nodes, fetchedAt: time.Now()}
	}
}

func (m model) tickCmd() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m *model) recomputeVisibleNodes() {
	filtered := make([]api.Node, 0, len(m.nodes))
	needle := strings.ToLower(strings.TrimSpace(m.activeFilter))

	for _, node := range m.nodes {
		nodeCopy := node

		if needle == "" {
			filtered = append(filtered, nodeCopy)
			continue
		}

		matchedContainers := make([]api.Container, 0, len(node.Containers))
		for _, c := range node.Containers {
			namespace := strings.ToLower(c.Namespace)
			pod := strings.ToLower(c.PodName)
			if strings.Contains(namespace, needle) || strings.Contains(pod, needle) {
				matchedContainers = append(matchedContainers, c)
			}
		}

		if len(matchedContainers) == 0 {
			continue
		}

		nodeCopy.Containers = matchedContainers
		filtered = append(filtered, nodeCopy)
	}

	m.visibleNodes = filtered
	m.reselectNode()
}

func (m *model) reselectNode() {
	if len(m.visibleNodes) == 0 {
		m.selected = 0
		m.selectedName = ""
		return
	}

	if m.selectedName != "" {
		for i, node := range m.visibleNodes {
			if node.Name == m.selectedName {
				m.selected = i
				return
			}
		}
	}

	m.selected = 0
	m.selectedName = m.visibleNodes[0].Name
}

func (m model) sortLabel() string {
	switch m.sortBy {
	case sortByName:
		return "name"
	case sortByUsage:
		return "usage"
	case sortByLimit:
		return "limit"
	default:
		return "none"
	}
}

func (m model) resourceLabel() string {
	if m.resource == resourceCPU {
		return "cpu"
	}

	return "memory"
}

func sortMemoryContainers(containers []api.Container, sortBy sortMode) []api.Container {
	if len(containers) == 0 {
		return containers
	}

	sorted := make([]api.Container, len(containers))
	copy(sorted, containers)

	labelOf := func(c api.Container) string {
		return strings.ToLower(fmt.Sprintf("%s/%s:%s", c.Namespace, c.PodName, c.Name))
	}

	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
		leftLabel := labelOf(left)
		rightLabel := labelOf(right)

		switch sortBy {
		case sortByName:
			return leftLabel < rightLabel
		case sortByUsage:
			leftHasUtil := left.MemUtilization != -1
			rightHasUtil := right.MemUtilization != -1
			if leftHasUtil != rightHasUtil {
				return leftHasUtil
			}
			leftUsage := bytesToMB(left.MemWorkingSet)
			rightUsage := bytesToMB(right.MemWorkingSet)
			if leftHasUtil {
				leftUsage = left.MemUtilization
				rightUsage = right.MemUtilization
			}
			if leftUsage == rightUsage {
				return leftLabel < rightLabel
			}
			return leftUsage > rightUsage
		case sortByLimit:
			leftMax := left.Limits.MemoryByte
			rightMax := right.Limits.MemoryByte
			leftHasMax := leftMax > 0
			rightHasMax := rightMax > 0
			if leftHasMax != rightHasMax {
				return leftHasMax
			}
			if !leftHasMax {
				return leftLabel < rightLabel
			}
			if leftMax == rightMax {
				return leftLabel < rightLabel
			}
			return leftMax > rightMax
		default:
			return false
		}
	})

	return sorted
}

func sortCPUContainers(containers []api.Container, sortBy sortMode) []api.Container {
	if len(containers) == 0 {
		return containers
	}

	sorted := make([]api.Container, len(containers))
	copy(sorted, containers)

	labelOf := func(c api.Container) string {
		return strings.ToLower(fmt.Sprintf("%s/%s:%s", c.Namespace, c.PodName, c.Name))
	}

	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
		leftLabel := labelOf(left)
		rightLabel := labelOf(right)

		switch sortBy {
		case sortByName:
			return leftLabel < rightLabel
		case sortByUsage:
			leftHasUtil := left.CPUUtilization != -1
			rightHasUtil := right.CPUUtilization != -1
			if leftHasUtil != rightHasUtil {
				return leftHasUtil
			}
			leftUsage := left.CPURate
			rightUsage := right.CPURate
			if leftHasUtil {
				leftUsage = left.CPUUtilization
				rightUsage = right.CPUUtilization
			}
			if leftUsage == rightUsage {
				return leftLabel < rightLabel
			}
			return leftUsage > rightUsage
		case sortByLimit:
			leftMax := left.Limits.CPUMillis
			rightMax := right.Limits.CPUMillis
			leftHasMax := leftMax > 0
			rightHasMax := rightMax > 0
			if leftHasMax != rightHasMax {
				return leftHasMax
			}
			if !leftHasMax {
				return leftLabel < rightLabel
			}
			if leftMax == rightMax {
				return leftLabel < rightLabel
			}
			return leftMax > rightMax
		default:
			return false
		}
	})

	return sorted
}

func bytesToMB(bytes uint64) float64 {
	return float64(bytes) / 1024 / 1024
}

func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}
