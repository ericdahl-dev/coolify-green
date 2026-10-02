package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericdahl-dev/coolify-green/internal/coolify"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
[[instances]]
name = "studio"
url = "coolify.example.com/"
token = "abc"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Settings.PollInterval != DefaultPollInterval {
		t.Errorf("poll interval = %d", cfg.Settings.PollInterval)
	}
	if cfg.Settings.StuckThresholdMinutes != DefaultStuckThresholdMinutes {
		t.Errorf("stuck threshold = %d", cfg.Settings.StuckThresholdMinutes)
	}
	if cfg.Settings.DeployHistorySeconds != DefaultDeployHistorySeconds {
		t.Errorf("deploy history = %d", cfg.Settings.DeployHistorySeconds)
	}
	if cfg.Instances[0].URL != "https://coolify.example.com" {
		t.Errorf("url not normalized: %q", cfg.Instances[0].URL)
	}
	for _, kind := range []coolify.Kind{coolify.KindApplication, coolify.KindService, coolify.KindDatabase} {
		if !cfg.WatchesKind(kind) {
			t.Errorf("default watch should include %s", kind)
		}
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"no instances", "[settings]\npoll_interval_seconds = 30\n", "no [[instances]]"},
		{"missing name", "[[instances]]\nurl = \"https://x\"\n", "name is required"},
		{"bad url", "[[instances]]\nname = \"a\"\nurl = \"ftp://x\"\n", "http or https"},
		{"duplicate name", "[[instances]]\nname = \"a\"\nurl = \"https://x\"\n\n[[instances]]\nname = \"a\"\nurl = \"https://y\"\n", "duplicate"},
		{"bad poll", "[settings]\npoll_interval_seconds = -1\n[[instances]]\nname = \"a\"\nurl = \"https://x\"\n", "at least 1 second"},
		{"bad watch", "[settings]\nwatch = [\"widgets\"]\n[[instances]]\nname = \"a\"\nurl = \"https://x\"\n", "unknown resource kind"},
		{"bad webhook", "[[instances]]\nname = \"a\"\nurl = \"https://x\"\n\n[[webhooks]]\nurl = \"nope\"\n", "invalid url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestWatchesKindHonorsSetting(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[settings]
watch = ["apps"]

[[instances]]
name = "studio"
url = "https://coolify.example.com"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.WatchesKind(coolify.KindApplication) {
		t.Error("applications should be watched")
	}
	if cfg.WatchesKind(coolify.KindService) {
		t.Error("services should not be watched")
	}
}

func TestOverridesRoundTrip(t *testing.T) {
	path := writeConfig(t, "[[instances]]\nname = \"a\"\nurl = \"https://x\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsEnabled("abc") {
		t.Error("unknown uuid should default to enabled")
	}
	if err := cfg.SetEnabled("abc", "clawproxy.io", false); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.IsEnabled("abc") {
		t.Error("disable did not persist")
	}
	if reloaded.Overrides[0].Name != "clawproxy.io" {
		t.Errorf("name not written: %+v", reloaded.Overrides[0])
	}

	if err := reloaded.SetEnabled("abc", "clawproxy.io", true); err != nil {
		t.Fatal(err)
	}
	final, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Overrides) != 0 {
		t.Errorf("re-enabling should drop the override, got %+v", final.Overrides)
	}
}

func TestEnabledInstances(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
[[instances]]
name = "a"
url = "https://x"

[[instances]]
name = "b"
url = "https://y"
enabled = false
`))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.EnabledInstances()
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("EnabledInstances = %+v", got)
	}
}

func TestResolveTokenPrecedence(t *testing.T) {
	t.Setenv("CG_TEST_TOKEN", "from-env")
	t.Setenv("COOLIFY_API_TOKEN", "from-fallback")

	got, err := ResolveToken(Instance{Name: "a", TokenCommand: "printf 'from-command\\n'", TokenEnv: "CG_TEST_TOKEN", Token: "literal"})
	if err != nil || got != "from-command" {
		t.Errorf("command precedence: %q %v", got, err)
	}

	got, err = ResolveToken(Instance{Name: "a", TokenEnv: "CG_TEST_TOKEN", Token: "literal"})
	if err != nil || got != "from-env" {
		t.Errorf("env precedence: %q %v", got, err)
	}

	got, err = ResolveToken(Instance{Name: "a", Token: "literal"})
	if err != nil || got != "literal" {
		t.Errorf("literal: %q %v", got, err)
	}

	got, err = ResolveToken(Instance{Name: "a"})
	if err != nil || got != "from-fallback" {
		t.Errorf("fallback: %q %v", got, err)
	}
}

func TestResolveTokenErrors(t *testing.T) {
	t.Setenv("COOLIFY_API_TOKEN", "")
	if _, err := ResolveToken(Instance{Name: "a"}); err == nil {
		t.Error("want error when no token source is set")
	}
	if _, err := ResolveToken(Instance{Name: "a", TokenEnv: "CG_DEFINITELY_UNSET"}); err == nil {
		t.Error("want error when token_env is empty")
	}
	if _, err := ResolveToken(Instance{Name: "a", TokenCommand: "exit 3"}); err == nil {
		t.Error("want error when token_command fails")
	}
	if _, err := ResolveToken(Instance{Name: "a", TokenCommand: "true"}); err == nil {
		t.Error("want error when token_command prints nothing")
	}
}

func TestWriteStarter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	err := WriteStarter(path, Instance{Name: "studio", URL: "https://coolify.example.com", TokenEnv: "COOLIFY_API_KEY"})
	if err != nil {
		t.Fatalf("WriteStarter: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load written starter: %v", err)
	}
	if cfg.Instances[0].TokenEnv != "COOLIFY_API_KEY" {
		t.Errorf("token_env not written: %+v", cfg.Instances[0])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("config perms = %v, want 0600", info.Mode().Perm())
	}
}
