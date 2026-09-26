package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/coolify-green/internal/aggregator"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/fix"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

var (
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	normalStyle   = lipgloss.NewStyle()
	staleStyle    = lipgloss.NewStyle().Faint(true)
	hintStyle     = lipgloss.NewStyle().Faint(true)
	confirmStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226"))
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	resourceIndent = "      "
	logIndent      = "            "
)

const (
	selectionTimeout  = 10 * time.Second
	timerTickInterval = time.Second
	fixStatusDuration = 5 * time.Second
	// maxLogLines is how much of a deployment's tail is shown inline. The
	// full log lives in Coolify; this is for "what is it doing right now".
	maxLogLines = 12
)

type selectionExpiredMsg struct{}
type timerTickMsg struct{}
type fixDoneMsg struct{ err error }
type fixStatusExpiredMsg struct{}
type logsFetchedMsg struct {
	deploymentUUID string
	lines          []coolify.LogLine
	err            error
}

// FixAppliedMsg tells the parent model a fix succeeded, so it can re-poll.
type FixAppliedMsg struct{}

type fixState int

const (
	fixIdle fixState = iota
	fixConfirming
	fixExecuting
	fixShowResult
)

// ActionerFactory builds a fix.Actioner for a named instance.
type ActionerFactory func(instance string) (fix.Actioner, error)

// LogFetcher reads one deployment's log lines. It is called only when a row is
// expanded, never on the polling path.
type LogFetcher func(ctx context.Context, instance, deploymentUUID string) ([]coolify.LogLine, error)

type navItemKind int

const (
	navProject navItemKind = iota
	navResource
)

// navItem identifies one navigable row.
type navItem struct {
	kind        navItemKind
	projKey     string // state.ProjectState.Key(), not Name — names repeat
	resourceIdx int    // only meaningful for navResource
}

type logEntry struct {
	lines   []coolify.LogLine
	err     error
	loading bool
}

// Dashboard is the main screen.
type Dashboard struct {
	snapshot    state.Snapshot
	cursor      int
	expanded    map[string]bool // project key -> expanded
	resExpanded map[string]bool // project key + "/" + resource uuid -> expanded
	logs        map[string]logEntry
	// lastStoplight is the stoplight each project was last seen at, so
	// auto-expansion can be edge-triggered. Level-triggering would re-open a
	// row the user collapsed by hand on every poll.
	lastStoplight map[string]aggregator.Stoplight
	lastActivity  time.Time
	selectionFade bool

	actionerFactory ActionerFactory
	logFetcher      LogFetcher
	ctx             context.Context

	// height is the terminal height, used to window long lists around the
	// cursor. Zero means "unknown", which renders everything.
	height int

	fixStatus    fixState
	fixPlan      *fix.Plan
	fixResultMsg string
	fixErr       bool
}

// NewDashboard builds the dashboard around an initial snapshot.
func NewDashboard(snap state.Snapshot, actionerFactory ActionerFactory, logFetcher LogFetcher, ctx context.Context) Dashboard {
	d := Dashboard{
		expanded:        make(map[string]bool),
		resExpanded:     make(map[string]bool),
		logs:            make(map[string]logEntry),
		lastStoplight:   make(map[string]aggregator.Stoplight),
		lastActivity:    time.Now(),
		actionerFactory: actionerFactory,
		logFetcher:      logFetcher,
		ctx:             ctx,
	}
	d.applySnapshot(snap)
	return d
}

// needsAttention reports whether a project should have its row opened for the
// user: something in it is broken or moving.
func needsAttention(s aggregator.Stoplight) bool {
	return s == aggregator.StoplightRed || s == aggregator.StoplightYellow
}

// applySnapshot installs a new snapshot, auto-expanding projects that have
// just started needing attention and collapsing those that have recovered.
// Expansion changes only when a project's stoplight changes (or on its first
// sighting), so a row the user collapsed by hand stays collapsed until
// something actually happens to it.
func (d *Dashboard) applySnapshot(snap state.Snapshot) {
	prev := d.currentNavItem()

	d.snapshot = snap

	seen := make(map[string]struct{}, len(snap.Projects))
	for _, proj := range snap.Projects {
		key := proj.Key()
		seen[key] = struct{}{}
		light := proj.Stoplight()
		if last, ok := d.lastStoplight[key]; ok && last == light {
			continue
		}
		d.lastStoplight[key] = light
		d.expanded[key] = needsAttention(light)
	}
	// A project that disappeared is a first sighting again if it returns.
	for key := range d.lastStoplight {
		if _, ok := seen[key]; !ok {
			delete(d.lastStoplight, key)
		}
	}

	d.restoreCursor(prev)
}

// restoreCursor puts the cursor back on the row it was on before the rebuild.
// Auto-expansion inserts rows above it, so the index alone is meaningless. A
// resource row that disappeared falls back to its project row.
func (d *Dashboard) restoreCursor(prev *navItem) {
	items := d.buildNavList()
	if prev == nil || len(items) == 0 {
		if d.cursor >= len(items) {
			d.cursor = max(len(items)-1, 0)
		}
		return
	}
	fallback := -1
	for i, it := range items {
		if it == *prev {
			d.cursor = i
			return
		}
		if fallback < 0 && it.kind == navProject && it.projKey == prev.projKey {
			fallback = i
		}
	}
	if fallback >= 0 {
		d.cursor = fallback
		return
	}
	if d.cursor >= len(items) {
		d.cursor = len(items) - 1
	}
}

func selectionTimeoutCmd() tea.Cmd {
	return tea.Tick(selectionTimeout, func(time.Time) tea.Msg { return selectionExpiredMsg{} })
}

func timerTickCmd() tea.Cmd {
	return tea.Tick(timerTickInterval, func(time.Time) tea.Msg { return timerTickMsg{} })
}

func fixStatusExpiredCmd() tea.Cmd {
	return tea.Tick(fixStatusDuration, func(time.Time) tea.Msg { return fixStatusExpiredMsg{} })
}

// Init starts the selection-fade and elapsed-timer tickers.
func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(selectionTimeoutCmd(), timerTickCmd())
}

// qualifyInstances reports whether rows should be prefixed with their instance
// name, which is only useful when more than one is configured.
func (d Dashboard) qualifyInstances() bool {
	return len(d.snapshot.Instances) > 1
}

// buildNavList returns the flat list of navigable rows for the current
// expansion state.
func (d Dashboard) buildNavList() []navItem {
	var items []navItem
	for _, projIdx := range state.SortedProjects(d.snapshot.Projects) {
		proj := d.snapshot.Projects[projIdx]
		items = append(items, navItem{kind: navProject, projKey: proj.Key()})
		if !d.expanded[proj.Key()] {
			continue
		}
		for i := range proj.Resources {
			items = append(items, navItem{kind: navResource, projKey: proj.Key(), resourceIdx: i})
		}
	}
	return items
}

func (d Dashboard) currentNavItem() *navItem {
	items := d.buildNavList()
	if d.cursor < 0 || d.cursor >= len(items) {
		return nil
	}
	item := items[d.cursor]
	return &item
}

func (d Dashboard) projectByKey(key string) *state.ProjectState {
	for i := range d.snapshot.Projects {
		if d.snapshot.Projects[i].Key() == key {
			p := d.snapshot.Projects[i]
			return &p
		}
	}
	return nil
}

// SelectedProject returns the project the cursor is on or inside.
func (d Dashboard) SelectedProject() *state.ProjectState {
	item := d.currentNavItem()
	if item == nil {
		return nil
	}
	return d.projectByKey(item.projKey)
}

// SelectedResource returns the resource under the cursor, if the cursor is on
// a resource row.
func (d Dashboard) SelectedResource() *state.ResourceState {
	item := d.currentNavItem()
	if item == nil || item.kind != navResource {
		return nil
	}
	proj := d.projectByKey(item.projKey)
	if proj == nil || item.resourceIdx >= len(proj.Resources) {
		return nil
	}
	r := proj.Resources[item.resourceIdx]
	return &r
}

// SelectedURL is the Coolify web UI link for the current row: the deployment
// when one is selected and in view, otherwise the resource, otherwise the
// project's instance.
func (d Dashboard) SelectedURL() string {
	proj := d.SelectedProject()
	if proj == nil {
		return ""
	}
	if r := d.SelectedResource(); r != nil {
		if r.Deploy != nil && r.Deploy.URL != "" && d.resExpanded[proj.Key()+"/"+r.UUID] {
			return r.Deploy.URL
		}
		return coolify.ResourceURL(proj.InstanceURL, r.Kind, proj.UUID, proj.EnvironmentUUID, r.UUID)
	}
	if proj.InstanceURL == "" {
		return ""
	}
	if proj.UUID == "" || proj.EnvironmentUUID == "" {
		return proj.InstanceURL + "/dashboard"
	}
	return fmt.Sprintf("%s/project/%s/environment/%s", proj.InstanceURL, proj.UUID, proj.EnvironmentUUID)
}

// Update handles one message.
func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.height = msg.Height

	case tea.KeyMsg:
		return d.handleKey(msg)

	case selectionExpiredMsg:
		if time.Since(d.lastActivity) >= selectionTimeout {
			d.selectionFade = true
		}

	case timerTickMsg:
		return d, timerTickCmd()

	case logsFetchedMsg:
		entry := logEntry{lines: msg.lines, err: msg.err}
		d.logs[msg.deploymentUUID] = entry

	case fixDoneMsg:
		d.fixStatus = fixShowResult
		if msg.err != nil {
			d.fixResultMsg = fmt.Sprintf("fix failed: %v", msg.err)
			d.fixErr = true
			return d, fixStatusExpiredCmd()
		}
		d.fixResultMsg = "✓ " + d.fixPlan.Kind.String()
		d.fixErr = false
		return d, tea.Batch(fixStatusExpiredCmd(), func() tea.Msg { return FixAppliedMsg{} })

	case fixStatusExpiredMsg:
		d.fixStatus = fixIdle
		d.fixPlan = nil
		d.fixResultMsg = ""

	case state.Snapshot:
		d.applySnapshot(msg)
		// Drop cached logs for deployments that are still running so the
		// next expand shows fresh output rather than a frozen tail.
		for _, proj := range msg.Projects {
			for _, r := range proj.Resources {
				if r.Deploy != nil && r.Deploy.InFlight() && d.resExpanded[proj.Key()+"/"+r.UUID] {
					delete(d.logs, r.Deploy.UUID)
				}
			}
		}
	}
	return d, nil
}

func (d Dashboard) handleKey(msg tea.KeyMsg) (Dashboard, tea.Cmd) {
	// While confirming a fix, only enter and esc mean anything.
	if d.fixStatus == fixConfirming {
		switch msg.String() {
		case "enter":
			d.fixStatus = fixExecuting
			plan := d.fixPlan
			factory := d.actionerFactory
			ctx := d.ctx
			return d, func() tea.Msg {
				actioner, err := factory(plan.Instance)
				if err != nil {
					return fixDoneMsg{err: fmt.Errorf("connect to %s: %w", plan.Instance, err)}
				}
				return fixDoneMsg{err: fix.Execute(ctx, plan, actioner)}
			}
		case "esc":
			d.fixStatus = fixIdle
			d.fixPlan = nil
		}
		return d, nil
	}
	if d.fixStatus == fixExecuting {
		return d, nil
	}

	d.lastActivity = time.Now()
	d.selectionFade = false
	count := len(d.buildNavList())

	switch msg.String() {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
	case "down", "j":
		if d.cursor < count-1 {
			d.cursor++
		}
	case "home", "g":
		d.cursor = 0
	case "end", "G":
		if count > 0 {
			d.cursor = count - 1
		}
	case "enter", " ":
		return d.toggleSelected()
	case "f":
		if proj := d.SelectedProject(); proj != nil {
			if plan := fix.PlanFor(*proj, fix.StalledDeployThreshold); plan != nil {
				d.fixStatus = fixConfirming
				d.fixPlan = plan
			}
		}
	}
	return d, selectionTimeoutCmd()
}

func (d Dashboard) toggleSelected() (Dashboard, tea.Cmd) {
	item := d.currentNavItem()
	if item == nil {
		return d, selectionTimeoutCmd()
	}
	switch item.kind {
	case navProject:
		d.expanded[item.projKey] = !d.expanded[item.projKey]
		if count := len(d.buildNavList()); d.cursor >= count && count > 0 {
			d.cursor = count - 1
		}
		return d, selectionTimeoutCmd()

	case navResource:
		proj := d.projectByKey(item.projKey)
		if proj == nil || item.resourceIdx >= len(proj.Resources) {
			return d, selectionTimeoutCmd()
		}
		r := proj.Resources[item.resourceIdx]
		key := item.projKey + "/" + r.UUID
		d.resExpanded[key] = !d.resExpanded[key]
		if d.resExpanded[key] && r.Deploy != nil && r.Deploy.UUID != "" {
			if entry, ok := d.logs[r.Deploy.UUID]; !ok || (entry.err != nil && !entry.loading) {
				d.logs[r.Deploy.UUID] = logEntry{loading: true}
				return d, tea.Batch(selectionTimeoutCmd(), d.fetchLogsCmd(proj.Instance, r.Deploy.UUID))
			}
		}
	}
	return d, selectionTimeoutCmd()
}

func (d Dashboard) fetchLogsCmd(instance, deploymentUUID string) tea.Cmd {
	fetch := d.logFetcher
	ctx := d.ctx
	return func() tea.Msg {
		if fetch == nil {
			return logsFetchedMsg{deploymentUUID: deploymentUUID, err: errors.New("log fetching is unavailable")}
		}
		lines, err := fetch(ctx, instance, deploymentUUID)
		return logsFetchedMsg{deploymentUUID: deploymentUUID, lines: lines, err: err}
	}
}

// View renders the dashboard with its title. The root model normally calls
// BodyView and supplies its own title line with the spinner.
func (d Dashboard) View() string {
	return TitleLine(false, "") + d.BodyView()
}

// BodyView renders the dashboard without the app title.
func (d Dashboard) BodyView() string {
	lines, cursorLine := d.bodyLines()
	lines = d.window(lines, cursorLine)
	return strings.Join(lines, "\n") + "\n\n" + d.hintLine()
}

// bodyLines renders every row and reports which line the cursor is on, so the
// viewport can keep the selection visible.
func (d Dashboard) bodyLines() ([]string, int) {
	var lines []string
	cursorLine := -1

	for _, inst := range d.snapshot.Instances {
		if inst.Err == nil {
			continue
		}
		lines = append(lines, errorStyle.Render(fmt.Sprintf("  ⚠ %s unreachable: %v", inst.Name, inst.Err)))
		if isAuthError(inst.Err) {
			lines = append(lines, hintStyle.Render("    check the instance's API token in the config"))
		}
	}

	if len(d.snapshot.Projects) == 0 {
		lines = append(lines, staleStyle.Render("  No projects discovered yet."))
	}

	navList := d.buildNavList()
	qualify := d.qualifyInstances()

	for _, projIdx := range state.SortedProjects(d.snapshot.Projects) {
		proj := d.snapshot.Projects[projIdx]
		expanded := d.expanded[proj.Key()]

		triangle := "▶"
		if expanded {
			triangle = "▼"
		}
		row := projectRow(proj, qualify)
		if d.isSelected(navList, navItem{kind: navProject, projKey: proj.Key()}) {
			cursorLine = len(lines)
			lines = append(lines, selectedStyle.Render(triangle+" "+row))
		} else {
			lines = append(lines, normalStyle.Render("  "+row))
		}

		if expanded {
			resourceLines, resourceCursor := d.resourceLines(proj, navList)
			if resourceCursor >= 0 {
				cursorLine = len(lines) + resourceCursor
			}
			lines = append(lines, resourceLines...)
		}
	}

	return lines, cursorLine
}

// window trims the rendered rows to what fits on screen, centred on the
// cursor. The title, the blank separator, and the hint line live outside the
// body, so the budget is the terminal height less three.
func (d Dashboard) window(lines []string, cursorLine int) []string {
	return windowLines(lines, cursorLine, d.height-3)
}

func (d Dashboard) isSelected(navList []navItem, want navItem) bool {
	if d.selectionFade {
		return false
	}
	for i, item := range navList {
		if item == want {
			return i == d.cursor
		}
	}
	return false
}

func (d Dashboard) hintLine() string {
	switch d.fixStatus {
	case fixConfirming:
		return confirmStyle.Render(fmt.Sprintf("%s  [enter] confirm  [esc] cancel", d.fixPlan.Description))
	case fixExecuting:
		return hintStyle.Render(fmt.Sprintf("%s…", d.fixPlan.Kind))
	case fixShowResult:
		if d.fixErr {
			return errorStyle.Render(d.fixResultMsg)
		}
		return successStyle.Render(d.fixResultMsg)
	default:
		return hintStyle.Render("↑/↓ navigate  enter/space expand  f fix  o open  r refresh  m manage  q quit  ? help")
	}
}

func projectRow(proj state.ProjectState, qualify bool) string {
	row := fmt.Sprintf("%s  %-28s", proj.Stoplight().String(), truncate(proj.FullName(qualify), 28))

	var parts []string
	for _, kind := range []coolify.Kind{coolify.KindApplication, coolify.KindService, coolify.KindDatabase} {
		light, count := proj.KindSummary(kind)
		if count == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s %d", kindHeading(kind), light.String(), count))
	}
	row += hintStyle.Render("  " + strings.Join(parts, "  "))

	if dep := proj.ActiveDeploy(); dep != nil {
		row += "  " + confirmStyle.Render(fmt.Sprintf("↻ %s %s", dep.Status, formatDuration(dep.Elapsed())))
	}

	if proj.IsStale() {
		age := time.Since(*proj.StaleAt).Round(time.Second)
		row += staleStyle.Render(fmt.Sprintf("  ⚠ last seen %s ago", age))
	}
	return row
}

func kindHeading(kind coolify.Kind) string {
	switch kind {
	case coolify.KindApplication:
		return "Apps"
	case coolify.KindService:
		return "Services"
	case coolify.KindDatabase:
		return "DBs"
	default:
		return string(kind)
	}
}

// kindSection is the heading above a group of resources inside an expanded
// project. The compact per-project summary uses the shorter kindHeading.
func kindSection(kind coolify.Kind) string {
	switch kind {
	case coolify.KindApplication:
		return "applications"
	case coolify.KindService:
		return "services"
	case coolify.KindDatabase:
		return "databases"
	default:
		return string(kind)
	}
}

func (d Dashboard) resourceLines(proj state.ProjectState, navList []navItem) ([]string, int) {
	var lines []string
	cursorLine := -1

	if len(proj.Resources) == 0 {
		return []string{staleStyle.Render(resourceIndent + "no monitored resources")}, -1
	}

	lastKind := coolify.Kind("")
	for i, r := range proj.Resources {
		if r.Kind != lastKind {
			lines = append(lines, normalStyle.Render(resourceIndent+kindSection(r.Kind)))
			lastKind = r.Kind
		}

		key := proj.Key() + "/" + r.UUID
		expanded := d.resExpanded[key]
		triangle := "  "
		if r.Deploy != nil {
			if expanded {
				triangle = "▼ "
			} else {
				triangle = "▶ "
			}
		}

		line := resourceIndent + "  " + triangle + resourceRow(r)
		if d.isSelected(navList, navItem{kind: navResource, projKey: proj.Key(), resourceIdx: i}) {
			cursorLine = len(lines)
			lines = append(lines, selectedStyle.Render(line))
		} else {
			lines = append(lines, line)
		}

		if expanded {
			lines = append(lines, d.resourceDetailLines(r)...)
		}
	}
	return lines, cursorLine
}

func resourceRow(r state.ResourceState) string {
	statusLabel, statusStyle := containerStatusLabel(r.Status)
	row := fmt.Sprintf("%s  %-26s %s", r.Stoplight().String(), truncate(r.Name, 26), statusStyle.Render(statusLabel))

	if r.Deploy != nil {
		label, style := deployStatusLabel(r.Deploy.Status)
		if label != "" {
			row += "  " + style.Render(label)
		}
		if r.Deploy.Commit != "" {
			row += " " + staleStyle.Render(r.Deploy.Commit)
		}
		if elapsed := r.Deploy.Elapsed(); elapsed > 0 {
			row += " " + staleStyle.Render(formatDuration(elapsed))
		}
	}
	return row
}

func (d Dashboard) resourceDetailLines(r state.ResourceState) []string {
	var lines []string

	if r.FQDN != "" {
		lines = append(lines, hintStyle.Render(logIndent+r.FQDN))
	}
	if r.GitRepository != "" {
		repo := r.GitRepository
		if r.GitBranch != "" {
			repo += "@" + r.GitBranch
		}
		lines = append(lines, hintStyle.Render(logIndent+repo))
	}
	if r.Deploy == nil {
		return lines
	}
	if r.Deploy.Subject != "" {
		lines = append(lines, hintStyle.Render(logIndent+truncate(r.Deploy.Subject, 80)))
	}

	entry, ok := d.logs[r.Deploy.UUID]
	switch {
	case !ok, entry.loading:
		lines = append(lines, staleStyle.Render(logIndent+"fetching deploy log…"))
	case entry.err != nil:
		lines = append(lines, errorStyle.Render(logIndent+"log unavailable: "+entry.err.Error()))
	case len(entry.lines) == 0:
		lines = append(lines, staleStyle.Render(logIndent+"no log output yet"))
	default:
		logLines := entry.lines
		if len(logLines) > maxLogLines {
			lines = append(lines, staleStyle.Render(fmt.Sprintf("%s… %d earlier lines", logIndent, len(logLines)-maxLogLines)))
			logLines = logLines[len(logLines)-maxLogLines:]
		}
		for _, l := range logLines {
			text := truncate(strings.TrimRight(l.Output, "\n"), 100)
			if l.IsError() {
				lines = append(lines, errorStyle.Render(logIndent+text))
			} else {
				lines = append(lines, staleStyle.Render(logIndent+text))
			}
		}
	}
	return lines
}

// isAuthError reports whether err looks like a rejected or missing API token.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, coolify.ErrUnauthorized) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"unauthorized", "forbidden", "token", "credentials", "401", "403"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func truncate(s string, max int) string {
	if max <= 1 || len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
