// Package cmd assembles the koochooloologin command tree and executes it.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/carlmjohnson/versioninfo"
	"github.com/urfave/cli/v3"

	profilecmd "github.com/1995parham/koochooloologin/internal/cmd/profile"
	storecmd "github.com/1995parham/koochooloologin/internal/cmd/store"
	"github.com/1995parham/koochooloologin/internal/infra/config"
)

// Execute builds the root command and runs it against os.Args, exiting non-zero
// on error.
func Execute() {
	root := &cli.Command{
		Name:    "koochooloologin",
		Usage:   "manage, launch, and share Chrome profiles with per-profile proxy, timezone, and fingerprint",
		Version: versioninfo.Short(),
		Flags: []cli.Flag{
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
				Name:    "identity",
				Aliases: []string{"i"},
				Usage:   "age or SSH private key used to decrypt secrets and bundles",
				Sources: cli.EnvVars(config.EnvPrefix + "IDENTITY"),
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
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
			if cmd.IsSet("identity") {
				cfg.IdentityPath = cmd.String("identity")
			}

			return config.With(ctx, &cfg), nil
		},
		Commands: []*cli.Command{
			profilecmd.Command(),
			storecmd.Command(),
		},
	}

	if err := root.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
