package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

// BackMsg is sent when the user leaves the manage screen.
type BackMsg struct{}

// ConfigChangedMsg is sent after the config is mutated, so the poller reloads.
type ConfigChangedMsg struct {
	Config *config.Config
}

type manageRowKind int

const (
	manageProject manageRowKind = iota
	manageResource
)

type manageRow struct {
	kind     manageRowKind
	uuid     string
	name     string
	label    string
	enabled  bool
	inherits bool // resource whose project is muted
}

// Manage is the screen for muting and unmuting discovered projects and
// resources. Everything on it comes from discovery — there is nothing to add
// by hand, so the only verbs are toggle and back.
type Manage struct {
	cfg       *config.Config
	inventory state.Inventory
	rows      []manageRow
	cursor    int
	err       string
	height    int
}

// NewManage builds the manage screen from the latest inventory.
func NewManage(cfg *config.Config, inv state.Inventory) Manage {
	m := Manage{cfg: cfg, inventory: inv}
	m.rebuild()
	return m
}

func (m *Manage) rebuild() {
	m.rows = nil
	qualify := false
	seenInstance := ""
	for _, proj := range m.inventory.Projects {
		if seenInstance != "" && proj.Instance != seenInstance {
			qualify = true
		}
		seenInstance = proj.Instance
	}

	for _, proj := range m.inventory.Projects {
		label := proj.Name
		if qualify && proj.Instance != "" {
			label = proj.Instance + " / " + proj.Name
		}
		projEnabled := m.cfg.IsEnabled(proj.UUID)
		m.rows = append(m.rows, manageRow{
			kind: manageProject, uuid: proj.UUID, name: proj.Name,
			label: label, enabled: projEnabled,
		})
		for _, r := range proj.Resources {
			m.rows = append(m.rows, manageRow{
				kind: manageResource, uuid: r.UUID, name: r.Name,
				label:    fmt.Sprintf("%-9s %-30s %s", r.Kind.Label(), truncate(r.Name, 30), r.Status),
				enabled:  m.cfg.IsEnabled(r.UUID),
				inherits: !projEnabled,
			})
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// Refresh replaces the inventory with a newer one, keeping the cursor where it
// was. The screen can be opened before the first poll has landed, so it has to
// fill in when discovery finishes rather than sit empty until reopened.
func (m Manage) Refresh(inv state.Inventory) Manage {
	m.inventory = inv
	m.rebuild()
	return m
}

// WithSize sets the terminal height so long inventories scroll. The manage
// screen is built on demand, after the window size message has already been
// delivered to the dashboard, so it has to be told.
func (m Manage) WithSize(height int) Manage {
	m.height = height
	return m
}

// Init satisfies the component interface; the manage screen has no startup
// work of its own.
func (m Manage) Init() tea.Cmd { return nil }

// Update handles one message.
func (m Manage) Update(msg tea.Msg) (Manage, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = size.Height
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		if len(m.rows) > 0 {
			m.cursor = len(m.rows) - 1
		}
	case "t", " ", "enter":
		return m.toggle()
	case "esc", "m", "q":
		return m, func() tea.Msg { return BackMsg{} }
	}
	return m, nil
}

func (m Manage) toggle() (Manage, tea.Cmd) {
	if m.cursor >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.cursor]
	if err := m.cfg.SetEnabled(row.uuid, row.name, !row.enabled); err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.err = ""
	m.rebuild()
	cfg := m.cfg
	return m, func() tea.Msg { return ConfigChangedMsg{Config: cfg} }
}

// View renders the manage screen.
func (m Manage) View() string {
	lines, cursorLine := m.bodyLines()
	// The title, the blank separator, and the hint line sit outside the body.
	lines = windowLines(lines, cursorLine, m.height-3)

	out := strings.Join(lines, "\n")
	if m.err != "" {
		out += "\n" + errorStyle.Render("  ⚠ "+m.err)
	}
	return out + "\n\n" + hintStyle.Render(
		fmt.Sprintf("↑/↓ navigate  t/space toggle  esc back    config: %s", abbreviateHome(m.cfg.Path())))
}

func (m Manage) bodyLines() ([]string, int) {
	if len(m.rows) == 0 {
		return []string{staleStyle.Render("  Nothing discovered yet — wait for the first poll to finish.")}, -1
	}

	lines := make([]string, 0, len(m.rows))
	cursorLine := -1
	for i, row := range m.rows {
		mark := "✓"
		style := normalStyle
		if !row.enabled {
			mark = "✗"
			style = staleStyle
		}

		var line string
		switch row.kind {
		case manageProject:
			line = fmt.Sprintf(" %s  %s", mark, truncate(row.label, 46))
		default:
			line = fmt.Sprintf("   %s  %s", mark, row.label)
			if row.enabled && row.inherits {
				line += staleStyle.Render("  (project muted)")
			}
		}

		if i == m.cursor {
			cursorLine = len(lines)
			lines = append(lines, selectedStyle.Render("▶"+line))
		} else {
			lines = append(lines, style.Render(" "+line))
		}
	}
	return lines, cursorLine
}
