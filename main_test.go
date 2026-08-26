package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/coolify-green/internal/state"
	"github.com/ericdahl-dev/coolify-green/internal/ui"
)

func TestRunCommandHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-help", "-h"} {
		var buf bytes.Buffer
		code, handled := runCommand([]string{arg}, &buf)
		if !handled || code != 0 {
			t.Errorf("%s: handled=%v code=%d", arg, handled, code)
		}
		if !strings.Contains(buf.String(), "coolify-green init") {
			t.Errorf("%s: help text missing usage:\n%s", arg, buf.String())
		}
		if !strings.Contains(buf.String(), "Config:") {
			t.Errorf("%s: help should name the config path:\n%s", arg, buf.String())
		}
	}
}

func TestRunCommandVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-version", "-v"} {
		var buf bytes.Buffer
		code, handled := runCommand([]string{arg}, &buf)
		if !handled || code != 0 {
			t.Errorf("%s: handled=%v code=%d", arg, handled, code)
		}
		if !strings.Contains(buf.String(), "coolify-green ") {
			t.Errorf("%s: version output = %q", arg, buf.String())
		}
	}
}

func TestRunCommandFallsThroughToDashboard(t *testing.T) {
	var buf bytes.Buffer
	if _, handled := runCommand(nil, &buf); handled {
		t.Error("no args should launch the dashboard")
	}
	if _, handled := runCommand([]string{"something-else"}, &buf); handled {
		t.Error("unknown args should launch the dashboard")
	}
}

func TestConfigPath(t *testing.T) {
	got := configPath()
	if filepath.Base(got) != "config.toml" {
		t.Errorf("configPath = %q", got)
	}
	if !strings.Contains(got, "coolify-green") {
		t.Errorf("configPath = %q, want it under a coolify-green directory", got)
	}
}

func TestWindowSizeReachesBothScreens(t *testing.T) {
	m := model{
		dashboard: ui.NewDashboard(state.NewSnapshot(nil, nil), nil, nil, context.Background()),
		manage:    ui.NewManage(nil, state.Inventory{}),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got, ok := updated.(model)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if got.winWidth != 120 || got.winHeight != 40 {
		t.Errorf("size = %dx%d", got.winWidth, got.winHeight)
	}
}
