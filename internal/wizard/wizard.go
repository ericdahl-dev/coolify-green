// Package wizard runs the interactive `coolify-green init` form.
package wizard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/coolify"
)

// ErrUserAborted is returned when the user cancels the form.
var ErrUserAborted = huh.ErrUserAborted

// Token source choices.
const (
	sourceEnv     = "env"
	sourceCommand = "command"
	sourceLiteral = "literal"
)

// RunInteractive collects one instance and writes a starter config to path.
func RunInteractive(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
	}

	name := "studio"
	url := strings.TrimSpace(os.Getenv("COOLIFY_API_URL"))
	source := sourceEnv
	envName := "COOLIFY_API_TOKEN"
	command := ""
	literal := ""

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Instance name").
				Description("A label for this Coolify instance (e.g. studio, homelab).").
				Value(&name).
				Validate(required("instance name")),
			huh.NewInput().
				Title("Instance URL").
				Description("Base URL of the Coolify web UI, e.g. https://coolify.example.com").
				Value(&url).
				Validate(func(s string) error {
					_, err := coolify.NormalizeBaseURL(s)
					return err
				}),
		).Title("coolify-green init · Instance").Description("Where your Coolify lives"),

		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Where should the API token come from?").
				Options(
					huh.NewOption("An environment variable", sourceEnv),
					huh.NewOption("A command (e.g. a secrets manager)", sourceCommand),
					huh.NewOption("Written into the config file", sourceLiteral),
				).
				Value(&source),
		).Title("coolify-green init · Token").Description("Create one under Settings → API Tokens"),

		huh.NewGroup(
			huh.NewInput().
				Title("Environment variable name").
				Description("coolify-green reads the token from this variable at startup.").
				Value(&envName).
				Validate(required("variable name")),
		).WithHideFunc(func() bool { return source != sourceEnv }),

		huh.NewGroup(
			huh.NewInput().
				Title("Token command").
				Description("Shell command whose output is the token, e.g. `op read op://vault/coolify/token`.").
				Value(&command).
				Validate(required("token command")),
		).WithHideFunc(func() bool { return source != sourceCommand }),

		huh.NewGroup(
			huh.NewInput().
				Title("API token").
				Description("Stored in the config file, which is written 0600.").
				EchoMode(huh.EchoModePassword).
				Value(&literal).
				Validate(required("API token")),
		).WithHideFunc(func() bool { return source != sourceLiteral }),
	)

	if err := form.Run(); err != nil {
		return err
	}

	inst := config.Instance{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)}
	switch source {
	case sourceEnv:
		inst.TokenEnv = strings.TrimSpace(envName)
	case sourceCommand:
		inst.TokenCommand = strings.TrimSpace(command)
	default:
		inst.Token = strings.TrimSpace(literal)
	}

	if err := config.WriteStarter(path, inst); err != nil {
		return err
	}

	// A config that cannot reach the instance is the most common first-run
	// problem, so say so now rather than at the empty dashboard.
	if err := verify(inst); err != nil {
		fmt.Fprintf(os.Stderr, "Wrote %s, but the connection check failed: %v\n", path, err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Wrote %s — connection to %s verified.\n", path, inst.URL)
	return nil
}

func verify(inst config.Instance) error {
	token, err := config.ResolveToken(inst)
	if err != nil {
		return err
	}
	client, err := coolify.New(inst.URL, token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	projects, err := client.Projects(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Found %d project(s).\n", len(projects))
	return nil
}

func required(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(what + " is required")
		}
		return nil
	}
}
