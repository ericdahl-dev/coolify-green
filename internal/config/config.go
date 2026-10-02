// Package config loads and writes the coolify-green TOML config file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
)

// Defaults for [settings].
const (
	DefaultPollInterval           = 30
	DefaultStuckThresholdMinutes  = 30
	DefaultDeployHistorySeconds   = 300
	DefaultTopologyRefreshSeconds = 600
)

// DefaultWatch is the set of resource kinds monitored when [settings].watch is
// not given.
var DefaultWatch = []string{"applications", "services", "databases"}

// Settings holds instance-wide polling behavior.
type Settings struct {
	PollInterval int `toml:"poll_interval_seconds"`
	// StuckThresholdMinutes is how long a resource stays broken or a deploy
	// stays unfinished before it fires a webhook.
	StuckThresholdMinutes int `toml:"stuck_threshold_minutes"`
	// DeployHistorySeconds is how often the last-known deployment of each
	// application is re-read. Active deploys are picked up every cycle from
	// the instance-wide endpoint; this only refreshes finished ones, which is
	// the expensive call because Coolify inlines the build log.
	DeployHistorySeconds int `toml:"deploy_history_interval_seconds"`
	// TopologyRefreshSeconds is how often the project/environment map is
	// rebuilt. It changes only when projects are added or renamed.
	TopologyRefreshSeconds int `toml:"topology_refresh_interval_seconds"`
	// Watch limits which resource kinds are monitored.
	Watch []string `toml:"watch"`
}

// Instance is one Coolify instance to monitor.
type Instance struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
	// Token is a literal API token. Prefer TokenEnv or TokenCommand so the
	// secret does not sit in the config file.
	Token string `toml:"token,omitempty"`
	// TokenEnv names an environment variable holding the token.
	TokenEnv string `toml:"token_env,omitempty"`
	// TokenCommand is a shell command whose stdout is the token, e.g. a
	// secrets-manager read.
	TokenCommand string `toml:"token_command,omitempty"`
	Enabled      *bool  `toml:"enabled,omitempty"`
}

// IsEnabled reports whether the instance should be polled.
func (i Instance) IsEnabled() bool { return i.Enabled == nil || *i.Enabled }

// Override mutes a discovered project or resource. Discovery is automatic, so
// the config only records the exceptions.
type Override struct {
	UUID string `toml:"uuid"`
	// Name is written for readability; matching is always on UUID.
	Name    string `toml:"name,omitempty"`
	Enabled bool   `toml:"enabled"`
}

// Webhook is an endpoint that receives stuck-resource events. Secret is
// optional; when set, requests are signed with HMAC-SHA256 over the body.
type Webhook struct {
	URL    string `toml:"url"`
	Secret string `toml:"secret,omitempty"`
}

// Config is the parsed config file.
type Config struct {
	Settings  Settings   `toml:"settings"`
	Instances []Instance `toml:"instances"`
	Overrides []Override `toml:"overrides"`
	Webhooks  []Webhook  `toml:"webhooks"`

	path string
}

// Load reads and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the user's own config
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s — run `coolify-green init` to create one", path)
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.path = path
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaultsAndValidate() error {
	if c.Settings.PollInterval == 0 {
		c.Settings.PollInterval = DefaultPollInterval
	}
	if c.Settings.PollInterval < 1 {
		return fmt.Errorf("poll_interval_seconds must be at least 1 second")
	}
	if c.Settings.StuckThresholdMinutes == 0 {
		c.Settings.StuckThresholdMinutes = DefaultStuckThresholdMinutes
	}
	if c.Settings.StuckThresholdMinutes < 0 {
		return fmt.Errorf("stuck_threshold_minutes must not be negative")
	}
	if c.Settings.DeployHistorySeconds == 0 {
		c.Settings.DeployHistorySeconds = DefaultDeployHistorySeconds
	}
	if c.Settings.DeployHistorySeconds < 0 {
		return fmt.Errorf("deploy_history_interval_seconds must not be negative")
	}
	if c.Settings.TopologyRefreshSeconds == 0 {
		c.Settings.TopologyRefreshSeconds = DefaultTopologyRefreshSeconds
	}
	if c.Settings.TopologyRefreshSeconds < 0 {
		return fmt.Errorf("topology_refresh_interval_seconds must not be negative")
	}
	if len(c.Settings.Watch) == 0 {
		c.Settings.Watch = append([]string(nil), DefaultWatch...)
	}
	for _, w := range c.Settings.Watch {
		if _, ok := normalizeKind(w); !ok {
			return fmt.Errorf("settings.watch: unknown resource kind %q (want applications, services, or databases)", w)
		}
	}

	if len(c.Instances) == 0 {
		return fmt.Errorf("no [[instances]] configured — run `coolify-green init`")
	}
	seen := make(map[string]bool, len(c.Instances))
	for i := range c.Instances {
		inst := &c.Instances[i]
		if strings.TrimSpace(inst.Name) == "" {
			return fmt.Errorf("instances[%d]: name is required", i)
		}
		if seen[inst.Name] {
			return fmt.Errorf("instances[%d]: duplicate instance name %q", i, inst.Name)
		}
		seen[inst.Name] = true
		normalized, err := coolify.NormalizeBaseURL(inst.URL)
		if err != nil {
			return fmt.Errorf("instances[%d] (%s): %w", i, inst.Name, err)
		}
		inst.URL = normalized
	}

	for i, wh := range c.Webhooks {
		if wh.URL == "" {
			return fmt.Errorf("webhooks[%d]: url is required", i)
		}
		u, err := url.ParseRequestURI(wh.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("webhooks[%d]: invalid url %q (must be http or https)", i, wh.URL)
		}
	}
	return nil
}

// normalizeKind accepts singular or plural kind names from the config and maps
// them to the coolify.Kind vocabulary.
func normalizeKind(s string) (coolify.Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "application", "applications", "app", "apps":
		return coolify.KindApplication, true
	case "service", "services":
		return coolify.KindService, true
	case "database", "databases", "db", "dbs":
		return coolify.KindDatabase, true
	default:
		return "", false
	}
}

// Path returns the file the config was loaded from.
func (c *Config) Path() string { return c.path }

// WatchesKind reports whether a resource kind is monitored.
func (c *Config) WatchesKind(kind coolify.Kind) bool {
	for _, w := range c.Settings.Watch {
		if k, ok := normalizeKind(w); ok && k == kind {
			return true
		}
	}
	return false
}

// EnabledInstances returns only the instances that are enabled.
func (c *Config) EnabledInstances() []Instance {
	var out []Instance
	for _, inst := range c.Instances {
		if inst.IsEnabled() {
			out = append(out, inst)
		}
	}
	return out
}

// IsEnabled reports whether a discovered project or resource should be polled.
// Discovery is opt-out: anything without an explicit override is monitored.
func (c *Config) IsEnabled(uuid string) bool {
	for _, o := range c.Overrides {
		if o.UUID == uuid {
			return o.Enabled
		}
	}
	return true
}

// SetEnabled records an enable/disable decision and saves. Re-enabling
// something drops its override rather than storing a redundant entry.
func (c *Config) SetEnabled(uuid, name string, enabled bool) error {
	for i, o := range c.Overrides {
		if o.UUID != uuid {
			continue
		}
		if enabled {
			c.Overrides = append(c.Overrides[:i], c.Overrides[i+1:]...)
		} else {
			c.Overrides[i].Enabled = false
			c.Overrides[i].Name = name
		}
		return c.Save()
	}
	if enabled {
		return nil // already the default
	}
	c.Overrides = append(c.Overrides, Override{UUID: uuid, Name: name, Enabled: false})
	return c.Save()
}

// Save writes the config back to the file it was loaded from.
func (c *Config) Save() error {
	if c.path == "" {
		return fmt.Errorf("config has no path set")
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(c.path, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// ResolveToken returns the API token for an instance, reading it from a
// command, an environment variable, or the config file in that order. Falling
// back to COOLIFY_API_TOKEN keeps the common single-instance case config-free.
func ResolveToken(inst Instance) (string, error) {
	if cmd := strings.TrimSpace(inst.TokenCommand); cmd != "" {
		out, err := exec.Command("sh", "-c", cmd).Output() // #nosec G204 -- command comes from the user's own config
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
					return "", fmt.Errorf("instance %q: token_command failed: %w: %s", inst.Name, err, stderr)
				}
			}
			return "", fmt.Errorf("instance %q: token_command failed: %w", inst.Name, err)
		}
		token := strings.TrimSpace(string(out))
		if token == "" {
			return "", fmt.Errorf("instance %q: token_command produced no output", inst.Name)
		}
		return token, nil
	}
	if envName := strings.TrimSpace(inst.TokenEnv); envName != "" {
		if token := strings.TrimSpace(os.Getenv(envName)); token != "" {
			return token, nil
		}
		return "", fmt.Errorf("instance %q: environment variable %s is empty", inst.Name, envName)
	}
	if token := strings.TrimSpace(inst.Token); token != "" {
		return token, nil
	}
	if token := strings.TrimSpace(os.Getenv("COOLIFY_API_TOKEN")); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("instance %q: no token — set token_command, token_env, token, or COOLIFY_API_TOKEN", inst.Name)
}

// WriteStarter writes a minimal valid config for one instance.
func WriteStarter(path string, inst Instance) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	cfg := Config{
		Settings: Settings{
			PollInterval:           DefaultPollInterval,
			StuckThresholdMinutes:  DefaultStuckThresholdMinutes,
			DeployHistorySeconds:   DefaultDeployHistorySeconds,
			TopologyRefreshSeconds: DefaultTopologyRefreshSeconds,
			Watch:                  append([]string(nil), DefaultWatch...),
		},
		Instances: []Instance{inst},
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0600)
}
