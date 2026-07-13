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
	modeDetail
	modeSortPrompt
	modeFilterPrompt
	modeHelp
)

type sortMode int

const (
	sortByName sortMode = iota
	sortByUsage
	sortByEfficiency
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

type metricColumn struct {
	header string
	width  int
	value  func(api.Container) string
}

type tickMsg struct{}

type model struct {
	forwarder             *kube.Forwarder
	interval              time.Duration
	styles                ui.Styles
	nodes                 []api.Node
	visibleNodes          []api.Node
	visibleContainers     []api.Container
	selectedNode          int
	selectedNodeName      string
	selectedContainer     int
	selectedContainerName string
	updating              bool
	lastUpdated           time.Time
	lastErr               string
	mode                  inputMode
	sortBy                sortMode
	filterInput           string
	activeFilter          string
	filterBeforeEdit      string
	resource              resourceMode
	width                 int
	height                int
}

func NewModel(forwarder *kube.Forwarder, interval time.Duration) tea.Model {
	return model{
		forwarder:    forwarder,
		interval:     interval,
		styles:       ui.NewStyles(),
		selectedNode: 0,
		updating:     true,
		mode:         modeNormal,
		sortBy:       sortByName,
		resource:     resourceMemory,
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
		m.recomputeVisibleContainers()
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
				m.selectedNode = min(m.selectedNode+1, len(m.visibleNodes)-1)
				m.selectedNodeName = m.visibleNodes[m.selectedNode].Name
			}
			m.recomputeVisibleContainers()
		case "k", "up":
			if len(m.visibleNodes) > 0 {
				m.selectedNode = max(m.selectedNode-1, 0)
				m.selectedNodeName = m.visibleNodes[m.selectedNode].Name
			}
			m.recomputeVisibleContainers()
		case "enter":
			m.mode = modeDetail
			m.selectedContainer = 0
		case "s":
			m.mode = modeSortPrompt
		case "/":
			m.mode = modeFilterPrompt
			m.filterBeforeEdit = m.activeFilter
			m.filterInput = m.activeFilter
		case "m":
			m.resource = resourceMemory
			m.recomputeVisibleContainers()
		case "c":
			m.resource = resourceCPU
			m.recomputeVisibleContainers()
		case "?":
			m.mode = modeHelp
		}
		return m, nil
	case modeDetail:
		switch msg.String() {
		case "j", "down":
			if len(m.visibleContainers) > 0 {
				m.selectedContainer = min(m.selectedContainer+1, len(m.visibleContainers)-1)
				m.selectedContainerName = m.visibleContainers[m.selectedContainer].Name
			}
		case "k", "up":
			if len(m.visibleContainers) > 0 {
				m.selectedContainer = max(m.selectedContainer-1, 0)
				m.selectedContainerName = m.visibleContainers[m.selectedContainer].Name
			}
		case "q":
			m.mode = modeNormal
		}
		return m, nil
	case modeSortPrompt:
		switch strings.ToLower(msg.String()) {
		case "esc":
			m.mode = modeNormal
		case "enter":
			m.sortBy = sortByName
			m.mode = modeNormal
			m.recomputeVisibleNodes()
			m.recomputeVisibleContainers()
		case "e":
			m.sortBy = sortByEfficiency
			m.mode = modeNormal
			m.recomputeVisibleNodes()
			m.recomputeVisibleContainers()
		case "u":
			m.sortBy = sortByUsage
			m.mode = modeNormal
			m.recomputeVisibleNodes()
			m.recomputeVisibleContainers()
		}
		return m, nil
	case modeFilterPrompt:
		switch msg.String() {
		case "esc":
			m.activeFilter = m.filterBeforeEdit
			m.filterInput = m.filterBeforeEdit
			m.mode = modeNormal
			m.recomputeVisibleNodes()
			m.recomputeVisibleContainers()
		case "enter":
			m.activeFilter = strings.TrimSpace(m.filterInput)
			m.mode = modeNormal
			m.recomputeVisibleNodes()
			m.recomputeVisibleContainers()
		case "backspace", "ctrl+h":
			m.filterInput = trimLastRune(m.filterInput)
			m.activeFilter = strings.TrimSpace(m.filterInput)
			m.recomputeVisibleNodes()
			m.recomputeVisibleContainers()
		default:
			if msg.Type == tea.KeyRunes {
				m.filterInput += string(msg.Runes)
				m.activeFilter = strings.TrimSpace(m.filterInput)
				m.recomputeVisibleNodes()
				m.recomputeVisibleContainers()
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

	contentWidth := max(0, m.width-m.styles.AppPanel.GetHorizontalFrameSize())

	title := m.styles.Title.Render("Kube-Thrifty")
	status := m.statusLine()
	header := title + lipgloss.PlaceHorizontal(contentWidth-lipgloss.Width(title), lipgloss.Right, status)

	inputLine := m.renderInputBuffer()

	var helpMenu string
	helpS := "?: help menu"
	if m.mode == modeHelp {
		helpMenu = m.styles.Popup.Render(helpS)
	} else {
		helpMenu = m.styles.Muted.Render(helpS)
	}

	subheader := inputLine + lipgloss.PlaceHorizontal(contentWidth-lipgloss.Width(inputLine), lipgloss.Right, helpMenu)

	panelGap := 1
	leftWidth := max(26, contentWidth/4)
	rightWidth := max(40, contentWidth-leftWidth-panelGap)

	nodePanel := m.styles.Panel.Width(leftWidth - 2).Render(m.renderNodeList(leftWidth - 4))
	nodeDetailPanel := m.styles.Panel.Width(leftWidth - 2).Render(m.renderNodeDetail())
	nodeBody := lipgloss.JoinVertical(lipgloss.Top, nodePanel, nodeDetailPanel)

	detailPanel := m.styles.Panel.Width(rightWidth - 2).Render(m.renderContainerList(rightWidth - 4))
	// TODO: render container details

	body := lipgloss.JoinHorizontal(lipgloss.Top, nodeBody, strings.Repeat(" ", panelGap), detailPanel)

	return m.styles.AppPanel.Render(lipgloss.JoinVertical(lipgloss.Left, header, "", subheader, body))
}

func (m model) statusLine() string {
	parts := []string{}

	if m.updating {
		parts = append(parts, m.styles.Muted.Render("updating"))
	}

	if !m.lastUpdated.IsZero() {
		parts = append(parts, m.styles.Muted.Render("last updated: "+m.lastUpdated.Format(time.DateTime)))
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

	lines := make([]string, 0, len(m.visibleNodes)+2)
	lines = append(lines, m.styles.Subtle.Render("Nodes"))
	lines = append(lines, "")
	for i, node := range m.visibleNodes {
		line := truncate(node.Name, width)
		if i == m.selectedNode {
			line = addSelectedLabel(line)
			lines = append(lines, m.styles.Selected.Render(line))
			continue
		}
		lines = append(lines, m.styles.NodeNormal.Render(line))
	}

	return strings.Join(lines, "\n")
}

func (m model) renderNodeDetail() string {
	if len(m.visibleNodes) == 0 {
		return m.styles.Subtle.Render("No node selected")
	}
	node := m.visibleNodes[m.selectedNode]
	memUsed := bytesToMB(node.MemUsed)
	memTotal := bytesToMB(node.MemTotal)
	header := m.styles.Subtle.Render("Node Details")
	nodeDetail := fmt.Sprintf("Node: %s\nCPU usage: %.1f%%\nMemory usage (MB): %.1f/%.1f", node.Name, node.CPUPercent, memUsed, memTotal)
	return header + "\n\n" + m.styles.Subtle.Render(nodeDetail)
}

func (m model) renderContainerList(width int) string {
	if len(m.visibleNodes) == 0 {
		return m.styles.Subtle.Render("No node selected")
	}

	if m.resource == resourceCPU {
		return m.renderContainerTable(
			width,
			"CPU Overview",
			"No container cpu data on selected node",
			cpuMetricColumns(),
			func(c api.Container) float64 {
				return c.CPUUtilization
			})
	}

	return m.renderContainerTable(
		width,
		"Memory Overview",
		"No container memory data on selected node",
		memoryMetricColumns(),
		func(c api.Container) float64 {
			return c.MemUtilization
		})
}

func (m model) renderContainerTable(width int, title string, emptyMessage string, columns []metricColumn, utilization func(api.Container) float64) string {
	if len(m.visibleContainers) == 0 {
		return m.styles.Subtle.Render(emptyMessage)
	}

	metricWidths := metricColumnWidths(columns)
	metricWidth := metricColumnsWidth(metricWidths)
	labelWidth := max(12, min(60, (width-metricWidth-1)/2))
	utilWidth := max(6, width-labelWidth-metricWidth-1)

	lines := []string{
		m.styles.Subtle.Render(title),
		"",
		m.styles.Subtle.Render(renderTableHeader(labelWidth, utilWidth, columns, metricWidths)),
		"",
	}

	for i, c := range m.visibleContainers {
		values := make([]string, 0, len(columns))
		for _, column := range columns {
			values = append(values, column.value(c))
		}

		truncatedLabel := truncate(containerLabel(c), labelWidth)
		labelS := fmt.Sprintf("%-*s", labelWidth, truncatedLabel)
		label := m.styles.BarLabel.Render(labelS)
		if m.mode == modeDetail && i == m.selectedContainer {
			labelS = fmt.Sprintf("%-*s", labelWidth, addSelectedLabel(truncatedLabel))
			label = m.styles.Selected.Render(labelS)
		}

		line := renderTableRow(
			"",
			formatUtilization(utilization(c), utilWidth),
			values,
			0,
			utilWidth,
			metricWidths,
		)
		lines = append(lines, label+m.styles.BarLabel.Render(line))
	}

	return strings.Join(lines, "\n")
}

func cpuMetricColumns() []metricColumn {
	return []metricColumn{
		{header: "Rate(mCPU)", width: 10, value: func(c api.Container) string { return formatCPURate(c.CPURate) }},
		{header: "Throttle", width: 8, value: func(c api.Container) string { return formatRatioPercent(c.CPUThrottledRatio) }},
	}
}

func memoryMetricColumns() []metricColumn {
	return []metricColumn{
		{header: "WSS(MB)", width: 7, value: func(c api.Container) string { return formatMB(c.MemWorkingSet) }},
		{header: "RSS(MB)", width: 7, value: func(c api.Container) string { return formatMB(c.MemResidentSet) }},
		{header: "OOM", width: 3, value: func(c api.Container) string { return fmt.Sprintf("%d", c.OOM) }},
	}
}

func metricColumnWidths(columns []metricColumn) []int {
	widths := make([]int, 0, len(columns))
	for _, column := range columns {
		widths = append(widths, max(column.width, len(column.header)))
	}
	return widths
}

func metricColumnsWidth(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width + 1
	}
	return total
}

func renderTableHeader(labelWidth int, utilWidth int, columns []metricColumn, metricWidths []int) string {
	values := make([]string, 0, len(columns))
	for _, column := range columns {
		values = append(values, column.header)
	}
	return renderTableRow("ns :: pod :: container", "Utilization", values, labelWidth, utilWidth, metricWidths)
}

func renderTableRow(label string, utilization string, values []string, labelWidth int, utilWidth int, metricWidths []int) string {
	var row strings.Builder
	fmt.Fprintf(&row, "%-*s %-*s", labelWidth, label, utilWidth, utilization)
	for i, value := range values {
		fmt.Fprintf(&row, " %*s", metricWidths[i], value)
	}
	return row.String()
}

func containerLabel(c api.Container) string {
	return fmt.Sprintf("%s :: %s :: %s", c.Namespace, c.PodName, c.Name)
}

func truncate(s string, width int) string {
	if width <= 3 || len(s) <= width {
		return s
	}
	return s[:width-3] + "..."
}

func addSelectedLabel(s string) string {
	if s[len(s)-1] == '.' {
		s = s[:len(s)-1] + "◀"
	} else {
		s += " ◀"
	}
	return s
}

func formatUtilization(value float64, width int) string {
	if value == -1 {
		return fmt.Sprintf("%-*s", width, "N/A")
	}

	percent := formatRatioPercent(value)
	if width < 18 {
		return fmt.Sprintf("%-*s", width, percent)
	}

	barWidth := max(10, width-len(percent)-1)
	return fmt.Sprintf("%-*s", width, fmt.Sprintf("%s %s", ui.ProgressBar(value, barWidth), percent))
}

func formatCPURate(rate float64) string {
	if rate < 1 {
		return "<1"
	}
	return fmt.Sprintf("%.0f", rate)
}

func formatRatioPercent(value float64) string {
	if value < 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.1f%%", value*100)
}

func formatMB(bytes uint64) string {
	return fmt.Sprintf("%.1f", bytesToMB(bytes))
}

func (m model) renderInputBuffer() string {
	switch m.mode {
	case modeDetail:
		return m.styles.Popup.Render("Detail Mode: j/k to navigate container list, q to quit")
	case modeSortPrompt:
		if m.resource == resourceCPU {
			return m.styles.Popup.Render(">> Sort by [e]fficiency, [u]sage of cpu, or press Enter for name") // cpu sorting prompt
		}
		return m.styles.Popup.Render(">> Sort by [e]fficiency, [u]sage of memory (wss), or press Enter for name") // mem sorting prompt
	case modeFilterPrompt:
		return m.styles.Popup.Render(fmt.Sprintf(">> Filter (Namespace/Pod): %s", m.filterInput))
	case modeHelp:
		return m.styles.Popup.Render("j/k: move  s: sort  /: filter(live)  c: cpu  m: memory  esc: cancel/close  q: quit")
	default:
		parts := []string{}
		parts = append(parts, "view: "+m.resourceLabel())
		if m.sortBy != sortByName {
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
		m.selectedNode = 0
		m.selectedNodeName = ""
		return
	}

	if m.selectedNodeName != "" {
		for i, node := range m.visibleNodes {
			if node.Name == m.selectedNodeName {
				m.selectedNode = i
				return
			}
		}
	}

	m.selectedNode = 0
	m.selectedNodeName = m.visibleNodes[0].Name
}

func (m *model) recomputeVisibleContainers() {
	if len(m.visibleNodes) == 0 {
		m.visibleContainers = nil
		m.selectedContainer = 0
		m.selectedContainerName = ""
		return
	}
	node := m.visibleNodes[m.selectedNode]
	if m.resource == resourceCPU {
		m.visibleContainers = sortCPUContainers(node.Containers, m.sortBy)
	} else {
		m.visibleContainers = sortMemoryContainers(node.Containers, m.sortBy)
	}

	m.reselectContainer()
}

func (m *model) reselectContainer() {
	if len(m.visibleContainers) == 0 {
		m.selectedContainer = 0
		m.selectedContainerName = ""
		return
	}

	if m.selectedContainerName != "" {
		for i, node := range m.visibleContainers {
			if node.Name == m.selectedContainerName {
				m.selectedContainer = i
				return
			}
		}
	}

	m.selectedContainer = 0
	m.selectedContainerName = m.visibleContainers[0].Name
}

func (m model) sortLabel() string {
	switch m.sortBy {
	case sortByEfficiency:
		return "utilization efficiency"
	case sortByUsage:
		if m.resource == resourceCPU { // cpu view
			return "cpu rate"
		}
		return "working set bytes" // memory view
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
			leftUsage := bytesToMB(left.MemWorkingSet)
			rightUsage := bytesToMB(right.MemWorkingSet)
			if leftUsage == rightUsage {
				return leftLabel < rightLabel
			}
			return leftUsage > rightUsage
		case sortByEfficiency:
			leftHasUtil := left.MemUtilization != -1
			rightHasUtil := right.MemUtilization != -1
			if leftHasUtil != rightHasUtil {
				return leftHasUtil
			}
			if !leftHasUtil {
				return leftLabel < rightLabel
			}
			return left.MemUtilization > right.MemUtilization
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
			leftUsage := left.CPURate
			rightUsage := right.CPURate
			if leftUsage == rightUsage {
				return leftLabel < rightLabel
			}
			return leftUsage > rightUsage
		case sortByEfficiency:
			leftHasUtil := left.CPUUtilization != -1
			rightHasUtil := right.CPUUtilization != -1
			if leftHasUtil != rightHasUtil {
				return leftHasUtil
			}
			if !leftHasUtil {
				return leftLabel < rightLabel
			}
			return left.CPUUtilization > right.CPUUtilization
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
