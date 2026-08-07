// Package browser provides the "browser" command group, which downloads and
// tracks the Chrome for Testing builds profiles launch.
package browser

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/code-chorus-io/hamzad/internal/infra/browser"
	"github.com/code-chorus-io/hamzad/internal/infra/chrome"
	"github.com/code-chorus-io/hamzad/internal/infra/config"
)

// errVersionRequired is returned by `remove` with no argument.
var errVersionRequired = errors.New("provide a version to remove")

// Command returns the "browser" command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "browser",
		Usage: "download and manage the Chrome builds profiles launch",
		Commands: []*cli.Command{
			installCommand(),
			listCommand(),
			removeCommand(),
			pathCommand(),
		},
	}
}

func installCommand() *cli.Command {
	return &cli.Command{
		Name:      "install",
		Usage:     "download a Chrome for Testing build (a channel or an exact version)",
		ArgsUsage: "[stable|beta|dev|canary|<version>]",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			build, err := browser.Resolve(ctx, cmd.Args().First())
			if err != nil {
				return err
			}

			fmt.Printf("chrome for testing %s (%s)\n", build.Version, build.Platform)

			meta, err := browser.Install(ctx, build, progressPrinter())
			if err != nil {
				return err
			}
			fmt.Println()

			path, err := browser.ExecPath(meta.Version)
			if err != nil {
				return err
			}

			fmt.Printf("installed %s\n", meta.Version)
			fmt.Printf("  binary: %s\n", path)
			fmt.Printf("  sha256: %s\n", meta.SHA256)

			// The download can succeed while the browser still cannot start, so
			// say so now rather than let it surface as a loader error at the
			// first `profile open`. It is a warning, not a failure: the fix is a
			// system package, not another download.
			if err := browser.Verify(ctx, path); err != nil {
				fmt.Printf("\nwarning: %v\n", err)

				return nil
			}

			fmt.Printf("pin it with 'chrome_version = \"%s\"' in the config, or HAMZAD_CHROME_VERSION\n", meta.Version)

			return nil
		},
	}
}

// progressPrinter renders download progress on one rewritten line, and only
// when the total is known — a spinner-less byte count is noise otherwise.
func progressPrinter() func(done, total int64) {
	var lastPercent int64 = -1

	return func(done, total int64) {
		if total <= 0 {
			return
		}
		percent := done * 100 / total
		if percent == lastPercent {
			return
		}
		lastPercent = percent
		fmt.Printf("\rdownloading… %3d%%  (%d/%d MiB)", percent, done>>20, total>>20)
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Usage:   "list installed browser builds",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			installs, err := browser.List()
			if err != nil {
				return err
			}
			if len(installs) == 0 {
				fmt.Println("no browsers installed — fetch one with 'browser install'")

				return nil
			}

			pinned := config.From(ctx).ChromeVersion
			w := tabwriter.NewWriter(cmd.Root().Writer, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "VERSION\tPLATFORM\tINSTALLED\tPINNED")
			for _, m := range installs {
				mark := "-"
				if m.Version == pinned {
					mark = "yes"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Version, m.Platform, m.Installed, mark)
			}

			return w.Flush()
		},
	}
}

func removeCommand() *cli.Command {
	return &cli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "delete an installed browser build",
		ArgsUsage: "<version>",
		Action: func(_ context.Context, cmd *cli.Command) error {
			version := cmd.Args().First()
			if version == "" {
				return errVersionRequired
			}

			if err := browser.Remove(version); err != nil {
				return err
			}

			fmt.Printf("removed %s\n", version)

			return nil
		},
	}
}

func pathCommand() *cli.Command {
	return &cli.Command{
		Name:      "path",
		Usage:     "print the browser binary a launch would use",
		ArgsUsage: "[version]",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if v := cmd.Args().First(); v != "" {
				path, err := browser.ExecPath(v)
				if err != nil {
					return err
				}
				fmt.Println(path)

				return nil
			}

			// Mirror what a launch does, so this answers the question actually
			// being asked: which binary will open next.
			cfg := config.From(ctx)
			path, err := chrome.Resolve(cfg.ChromePath, cfg.ChromeVersion)
			if err != nil {
				return err
			}
			fmt.Println(path)

			return nil
		},
	}
}
