// Package cmd assembles the koochooloologin command tree and executes it.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/carlmjohnson/versioninfo"
	"github.com/urfave/cli/v3"

	browsercmd "github.com/1995parham/koochooloologin/internal/cmd/browser"
	profilecmd "github.com/1995parham/koochooloologin/internal/cmd/profile"
	storecmd "github.com/1995parham/koochooloologin/internal/cmd/store"
	tuicmd "github.com/1995parham/koochooloologin/internal/cmd/tui"
	"github.com/1995parham/koochooloologin/internal/infra/config"
)

// Execute builds the root command and runs it against os.Args, exiting non-zero
// on error.
func Execute() {
	root := &cli.Command{
		Name:    "koochooloologin",
		Usage:   "manage, launch, and share Chrome profiles with per-profile proxy, timezone, and fingerprint",
		Version: versioninfo.Short(),
		Flags:  rootFlags(),
		Before: loadConfig,
		Commands: []*cli.Command{
			profilecmd.Command(),
			browsercmd.Command(),
			storecmd.Command(),
			tuicmd.Command(),
		},
	}

	if err := root.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// rootFlags are the global settings, each also readable from a KEL_-prefixed
// environment variable.
func rootFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Usage:   "path to a TOML config file",
			Sources: cli.EnvVars(config.EnvPrefix + "CONFIG"),
		},
		&cli.StringFlag{
			Name:    "store-dir",
			Usage:   "override the profile store directory",
			Sources: cli.EnvVars(config.EnvPrefix + "STORE_DIR"),
		},
		&cli.StringFlag{
			Name:    "chrome-path",
			Usage:   "override the Chrome/Chromium executable path",
			Sources: cli.EnvVars(config.EnvPrefix + "CHROME_PATH"),
		},
		&cli.StringFlag{
			Name:    "chrome-version",
			Usage:   "pin a managed Chrome build installed with 'browser install'",
			Sources: cli.EnvVars(config.EnvPrefix + "CHROME_VERSION"),
		},
		&cli.StringFlag{
			Name:    "identity",
			Aliases: []string{"i"},
			Usage:   "age or SSH private key used to decrypt secrets and bundles",
			Sources: cli.EnvVars(config.EnvPrefix + "IDENTITY"),
		},
	}
}

// loadConfig layers the file and environment configuration, then lets an
// explicitly-set flag win over both, and carries the result on the context.
func loadConfig(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	cfg, err := config.Load(cmd.String("config"))
	if err != nil {
		return ctx, err
	}

	if cmd.IsSet("store-dir") {
		cfg.StoreDir = cmd.String("store-dir")
	}
	if cmd.IsSet("chrome-path") {
		cfg.ChromePath = cmd.String("chrome-path")
	}
	if cmd.IsSet("chrome-version") {
		cfg.ChromeVersion = cmd.String("chrome-version")
	}
	if cmd.IsSet("identity") {
		cfg.IdentityPath = cmd.String("identity")
	}

	return config.With(ctx, &cfg), nil
}
