// Package store provides the "store" command group, which manages the profile
// store as a git repository and shares profiles by syncing with a remote.
package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/code-chorus-io/hamzad/internal/infra/config"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// errRecipientRequired is returned by `recipients add` with no arguments.
var errRecipientRequired = errors.New("provide at least one recipient key or file")

// Command returns the "store" command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "store",
		Usage: "manage and share the profile store via git",
		Commands: []*cli.Command{
			initCommand(),
			syncCommand(),
			statusCommand(),
			recipientsCommand(),
		},
	}
}

func storeFrom(ctx context.Context) *store.Store {
	return store.New(config.From(ctx).StoreDir)
}

// resolveRecipients turns an argument that is either a key literal (age1…,
// ssh-…) or a path to a file of keys into one or more recipient lines.
func resolveRecipients(arg string) ([]string, error) {
	if _, err := os.Stat(arg); err != nil {
		return []string{strings.TrimSpace(arg)}, nil
	}

	f, err := os.Open(arg) //nolint:gosec // recipient file path is provided by the operator on the CLI
	if err != nil {
		return nil, fmt.Errorf("opening recipient file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var keys []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keys = append(keys, line)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("scanning recipient file: %w", err)
	}

	return keys, nil
}

// addRecipients adds each resolved recipient to the store, returning how many
// were newly added.
func addRecipients(st *store.Store, args []string) (int, error) {
	added := 0
	for _, arg := range args {
		keys, err := resolveRecipients(arg)
		if err != nil {
			return added, err
		}
		for _, key := range keys {
			ok, err := st.AddRecipient(key)
			if err != nil {
				return added, err
			}
			if ok {
				added++
			}
		}
	}

	return added, nil
}

func initCommand() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "initialize the store as a git repository and seed encryption recipients",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "remote", Aliases: []string{"r"}, Usage: "git remote URL to share through (set as origin)"},
			&cli.StringSliceFlag{Name: "recipient", Usage: "extra age/ssh public key or key file to add as a recipient"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st := storeFrom(ctx)
			if err := st.InitRepo(ctx, cmd.String("remote")); err != nil {
				return err
			}

			seeded, err := seedRecipients(ctx, st, cmd.StringSlice("recipient"))
			if err != nil {
				return err
			}

			fmt.Printf("initialized store at %s\n", st.Dir)
			if r := st.Remote(ctx); r != "" {
				fmt.Printf("origin: %s\n", r)
			}
			fmt.Printf("recipients: %d configured (%d added now)\n", recipientCount(st), seeded)
			if recipientCount(st) == 0 {
				fmt.Println("note: no keys found — add one with 'store recipients add <key|file>' before storing secrets")
			}

			return nil
		},
	}
}

// seedRecipients adds the user's own SSH public key (derived from the identity
// path) plus any explicitly provided recipients.
func seedRecipients(ctx context.Context, st *store.Store, extra []string) (int, error) {
	args := extra

	pub := config.From(ctx).IdentityPath + ".pub"
	if _, err := os.Stat(pub); err == nil {
		args = append([]string{pub}, args...)
	}

	return addRecipients(st, args)
}

func recipientCount(st *store.Store) int {
	rs, _ := st.Recipients()

	return len(rs)
}

func syncCommand() *cli.Command {
	return &cli.Command{
		Name:  "sync",
		Usage: "commit local profile changes, then pull and push to the remote",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "message", Aliases: []string{"m"}, Usage: "commit message"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st := storeFrom(ctx)
			if err := st.Sync(ctx, cmd.String("message")); err != nil {
				return err
			}

			if st.Remote(ctx) == "" {
				fmt.Println("synced locally (no remote configured — set one with 'store init --remote')")
			} else {
				fmt.Println("synced with remote")
			}

			return nil
		},
	}
}

func statusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "show the store's git status",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st := storeFrom(ctx)
			fmt.Printf("store:  %s\n", st.Dir)

			if !st.IsRepo(ctx) {
				fmt.Println("git:    not initialized (run 'store init')")

				return nil
			}

			fmt.Printf("origin: %s\n", orNone(st.Remote(ctx)))

			dirty, err := st.Dirty(ctx)
			if err != nil {
				return err
			}
			if dirty {
				fmt.Println("state:  uncommitted changes — run 'store sync'")
			} else {
				fmt.Println("state:  clean")
			}

			return nil
		},
	}
}

func recipientsCommand() *cli.Command {
	return &cli.Command{
		Name:  "recipients",
		Usage: "manage the encryption recipients (who can decrypt shared secrets)",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "list configured recipients",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					rs, err := storeFrom(ctx).Recipients()
					if err != nil {
						return err
					}
					if len(rs) == 0 {
						fmt.Println("no recipients configured")

						return nil
					}
					for _, r := range rs {
						fmt.Println(r)
					}

					return nil
				},
			},
			{
				Name:      "add",
				Usage:     "add an age/ssh public key or a key file as a recipient",
				ArgsUsage: "<key-or-file>...",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return errRecipientRequired
					}

					added, err := addRecipients(storeFrom(ctx), args)
					if err != nil {
						return err
					}

					fmt.Printf("added %d recipient(s)\n", added)
					fmt.Println("re-encrypt existing secrets so new recipients can read them (re-run 'profile add --proxy' / 'profile push')")

					return nil
				},
			},
		},
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}

	return s
}
