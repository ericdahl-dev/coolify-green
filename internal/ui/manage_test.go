package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
	"github.com/ericdahl-dev/coolify-green/internal/state"
)

func testConfigFile(t *testing.T) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[[instances]]\nname = \"studio\"\nurl = \"https://coolify.test\"\ntoken = \"x\"\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func testInventory() state.Inventory {
	return state.Inventory{Projects: []state.InventoryProject{{
		Instance: "studio", UUID: "p1", Name: "Alpha", Enabled: true,
		Resources: []state.InventoryResource{
			{Kind: coolify.KindApplication, UUID: "a1", Name: "alpha-web", Status: "running:healthy", Enabled: true},
			{Kind: coolify.KindService, UUID: "s1", Name: "n8n", Status: "exited", Enabled: true},
		},
	}}}
}

func TestManageListsProjectsAndResources(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, testInventory())
	out := m.View()
	for _, want := range []string{"Alpha", "alpha-web", "n8n", "app", "service"} {
		if !strings.Contains(out, want) {
			t.Errorf("manage view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, cfg.Path()) {
		t.Errorf("manage view should show the config path:\n%s", out)
	}
}

func TestToggleMutesAndPersists(t *testing.T) {
	cfg, path := testConfigFile(t)
	m := NewManage(cfg, testInventory())
	m.cursor = 1 // alpha-web

	m, cmd := m.Update(key("t"))
	if cmd == nil {
		t.Fatal("toggle should emit ConfigChangedMsg")
	}
	if _, ok := cmd().(ConfigChangedMsg); !ok {
		t.Fatalf("unexpected msg %T", cmd())
	}
	if cfg.IsEnabled("a1") {
		t.Error("resource should now be muted in memory")
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.IsEnabled("a1") {
		t.Error("mute did not reach the config file")
	}
	if !strings.Contains(m.View(), "✗") {
		t.Errorf("muted row should render a cross:\n%s", m.View())
	}

	// Toggling back removes the override.
	m, _ = m.Update(key("t"))
	final, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !final.IsEnabled("a1") {
		t.Error("unmute did not persist")
	}
}

func TestMutedProjectMarksItsResources(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, testInventory())
	m.cursor = 0 // the project row
	m, _ = m.Update(key("t"))
	if !strings.Contains(m.View(), "(project muted)") {
		t.Errorf("resources of a muted project should say so:\n%s", m.View())
	}
}

func TestEscGoesBack(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, testInventory())
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc should emit a command")
	}
	if _, ok := cmd().(BackMsg); !ok {
		t.Errorf("unexpected msg %T", cmd())
	}
}

func TestManageNavigationStaysInBounds(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, testInventory())
	for i := 0; i < 10; i++ {
		m, _ = m.Update(key("j"))
	}
	if m.cursor != len(m.rows)-1 {
		t.Errorf("cursor = %d, want %d", m.cursor, len(m.rows)-1)
	}
	for i := 0; i < 10; i++ {
		m, _ = m.Update(key("k"))
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
}

func TestManageWithEmptyInventory(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, state.Inventory{})
	if !strings.Contains(m.View(), "Nothing discovered yet") {
		t.Errorf("empty state missing:\n%s", m.View())
	}
	// Toggling with no rows must not panic.
	if _, cmd := m.Update(key("t")); cmd != nil {
		t.Errorf("toggling an empty list should do nothing, got %T", cmd())
	}
}

func TestManageQualifiesNamesAcrossInstances(t *testing.T) {
	cfg, _ := testConfigFile(t)
	inv := testInventory()
	inv.Projects = append(inv.Projects, state.InventoryProject{
		Instance: "homelab", UUID: "p9", Name: "Alpha", Enabled: true,
	})
	m := NewManage(cfg, inv)
	if !strings.Contains(m.View(), "homelab / Alpha") {
		t.Errorf("second instance should be qualified:\n%s", m.View())
	}
}

func TestManageScrollsOnAShortTerminal(t *testing.T) {
	cfg, _ := testConfigFile(t)
	inv := testInventory()
	for i := 0; i < 40; i++ {
		inv.Projects[0].Resources = append(inv.Projects[0].Resources, state.InventoryResource{
			Kind: coolify.KindService, UUID: "extra" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Name: "svc", Status: "running:healthy", Enabled: true,
		})
	}
	m := NewManage(cfg, inv)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	if lines := strings.Count(m.View(), "\n"); lines > 12 {
		t.Errorf("manage used %d lines on a 12-line terminal:\n%s", lines, m.View())
	}
}

func TestManageFillsInWhenDiscoveryLands(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, state.Inventory{})
	if !strings.Contains(m.View(), "Nothing discovered yet") {
		t.Fatalf("expected the empty state:\n%s", m.View())
	}
	m = m.Refresh(testInventory())
	if !strings.Contains(m.View(), "alpha-web") {
		t.Errorf("refresh did not pick up the inventory:\n%s", m.View())
	}
}

func TestRefreshKeepsTheCursorInRange(t *testing.T) {
	cfg, _ := testConfigFile(t)
	m := NewManage(cfg, testInventory())
	m.cursor = 2
	m = m.Refresh(state.Inventory{Projects: testInventory().Projects[:1]})
	if m.cursor >= len(m.rows) {
		t.Errorf("cursor %d out of range for %d rows", m.cursor, len(m.rows))
	}
}
