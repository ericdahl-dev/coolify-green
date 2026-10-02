// Command coolify-green is a terminal dashboard for Coolify deployment and
// resource health.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	bspin "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/fix"
	"github.com/ericdahl-dev/coolify-green/internal/poller"
	"github.com/ericdahl-dev/coolify-green/internal/state"
	"github.com/ericdahl-dev/coolify-green/internal/ui"
	"github.com/ericdahl-dev/coolify-green/internal/wizard"
)

type screen int

const (
	screenDashboard screen = iota
	screenManage
)

type model struct {
	screen    screen
	dashboard ui.Dashboard
	manage    ui.Manage
	showHelp  bool

	cfg      *config.Config
	poller   *poller.Poller
	pollCh   chan state.Snapshot
	pollCtx  context.Context
	stopPoll func()

	winWidth  int
	winHeight int
	fetching  bool
	spinner   bspin.Model
}

func waitForSnapshot(ch <-chan state.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return snap
	}
}

func kickSpinner(s bspin.Model) tea.Cmd {
	return func() tea.Msg { return s.Tick() }
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForSnapshot(m.pollCh), m.dashboard.Init(), kickSpinner(m.spinner))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.winWidth, m.winHeight = msg.Width, msg.Height
		// Both screens need the size, not just the visible one: the manage
		// screen is built later, long after this message has been delivered.
		var dashCmd, manCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		m.manage, manCmd = m.manage.Update(msg)
		return m, tea.Batch(dashCmd, manCmd)

	case bspin.TickMsg:
		if !m.fetching {
			return m, nil
		}
		var sc tea.Cmd
		m.spinner, sc = m.spinner.Update(msg)
		return m, sc

	case ui.BackMsg:
		m.screen = screenDashboard
		return m, nil

	case ui.ConfigChangedMsg:
		m.cfg = msg.Config
		m.fetching = true
		m.poller.ReloadConfig(m.pollCtx, m.cfg, m.pollCh)
		return m, kickSpinner(m.spinner)

	case ui.FixAppliedMsg:
		m.fetching = true
		m.poller.ForceRefresh(m.pollCtx, m.pollCh)
		return m, kickSpinner(m.spinner)

	case tea.KeyMsg:
		if m.screen == screenManage {
			var manCmd tea.Cmd
			m.manage, manCmd = m.manage.Update(msg)
			return m, manCmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.stopPoll()
			return m, tea.Quit
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		case "esc":
			if m.showHelp {
				m.showHelp = false
				return m, nil
			}
		case "m":
			m.screen = screenManage
			m.manage = ui.NewManage(m.cfg, m.poller.Inventory()).WithSize(m.winHeight)
			return m, nil
		case "r":
			m.fetching = true
			// A manual refresh is also how you pick up a project that was
			// created since startup, so rebuild the project map too.
			m.poller.InvalidateTopology()
			m.poller.ForceRefresh(m.pollCtx, m.pollCh)
			cmds = append(cmds, kickSpinner(m.spinner))
		case "o":
			openInBrowser(m.dashboard.SelectedURL())
			return m, nil
		}

	case state.Snapshot:
		m.fetching = false
		cmds = append(cmds, waitForSnapshot(m.pollCh))
		if m.screen == screenManage {
			m.manage = m.manage.Refresh(m.poller.Inventory())
		}
	}

	if m.screen == screenManage {
		var manCmd tea.Cmd
		m.manage, manCmd = m.manage.Update(msg)
		cmds = append(cmds, manCmd)
	} else {
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		cmds = append(cmds, dashCmd)
	}
	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if m.showHelp {
		return ui.RenderHelp(m.winWidth)
	}
	title := ui.TitleLine(m.fetching, m.spinner.View())
	if m.screen == screenManage {
		return title + m.manage.View()
	}
	return title + m.dashboard.BodyView()
}

func openInBrowser(url string) {
	if url == "" {
		return
	}
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "coolify-green", "config.toml")
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "overwrite an existing config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := wizard.RunInteractive(configPath(), *force); err != nil {
		if errors.Is(err, wizard.ErrUserAborted) {
			return 1
		}
		fmt.Fprintf(os.Stderr, "coolify-green init: %v\n", err)
		return 1
	}
	return 0
}

// version is overwritten at build time via -ldflags "-X main.version=...".
// goreleaser sets it from the git tag; a plain `go build` leaves it as "dev".
var version = "dev"

func helpText() string {
	return `coolify-green — terminal dashboard for Coolify deploys and resource health

Usage:
  coolify-green            launch the dashboard
  coolify-green init       create a starter config
  coolify-green --version  print the version
  coolify-green --help     show this help

Config: ` + configPath() + `
`
}

// runCommand handles the non-TUI invocations. It reports whether args were
// handled here, along with the exit code to use when they were. Anything it
// does not recognize falls through to launching the dashboard.
func runCommand(args []string, out io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "init":
		return runInit(args[1:]), true
	case "help", "-help", "--help", "-h":
		_, _ = fmt.Fprint(out, helpText())
		return 0, true
	case "version", "-version", "--version", "-v":
		_, _ = fmt.Fprintf(out, "coolify-green %s\n", version)
		return 0, true
	}
	return 0, false
}

// clientFactory builds a live API client for one configured instance,
// resolving its token from the command, environment, or config.
func clientFactory(inst config.Instance) (poller.Fetcher, error) {
	token, err := config.ResolveToken(inst)
	if err != nil {
		return nil, err
	}
	return coolify.New(inst.URL, token)
}

func actionerFor(cfg *config.Config) ui.ActionerFactory {
	return func(instance string) (fix.Actioner, error) {
		for _, inst := range cfg.Instances {
			if inst.Name != instance {
				continue
			}
			token, err := config.ResolveToken(inst)
			if err != nil {
				return nil, err
			}
			return coolify.New(inst.URL, token)
		}
		return nil, fmt.Errorf("instance %q is not in the config", instance)
	}
}

func main() {
	if code, handled := runCommand(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}

	cfg, err := config.Load(configPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "coolify-green: %v\n", err)
		os.Exit(1)
	}

	p := poller.New(cfg, clientFactory)
	ctx, cancel := context.WithCancel(context.Background())

	// The poller closes its own channel on shutdown; the model reads from a
	// channel it can also write to via ForceRefresh, so bridge the two.
	writeCh := make(chan state.Snapshot, 4)
	readCh, stopPoller := p.Start(ctx)
	go func() {
		for snap := range readCh {
			writeCh <- snap
		}
	}()

	spin := bspin.New(
		bspin.WithSpinner(bspin.MiniDot),
		bspin.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("205"))),
	)

	m := model{
		screen:    screenDashboard,
		dashboard: ui.NewDashboard(p.Snapshot(), actionerFor(cfg), p.FetchDeploymentLogs, ctx),
		manage:    ui.NewManage(cfg, p.Inventory()),
		cfg:       cfg,
		poller:    p,
		pollCh:    writeCh,
		pollCtx:   ctx,
		stopPoll:  func() { cancel(); stopPoller() },
		winWidth:  80,
		fetching:  true,
		spinner:   spin,
	}

	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "coolify-green: %v\n", err)
		os.Exit(1)
	}
}
