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
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/code-chorus-io/hamzad/internal/infra/config"
	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// errRecipientRequired is returned by `recipients add` with no arguments.
var errRecipientRequired = errors.New("provide at least one recipient key or file")

// errReencryptFailed is returned when at least one artifact could not be
// re-encrypted, so the command exits non-zero even though the rest succeeded.
var errReencryptFailed = errors.New("some artifacts could not be re-encrypted")

// Command returns the "store" command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "store",
		Usage: "manage and share the profile store via git",
		Commands: []*cli.Command{
			initCommand(),
			listCommand(),
			syncCommand(),
			statusCommand(),
			recipientsCommand(),
			reencryptCommand(),
		},
	}
}

func storeFrom(ctx context.Context) *store.Store {
	return store.New(config.From(ctx).StoreDir)
}

// cryptFor builds the crypt handle for a store from the configured identity.
func cryptFor(ctx context.Context, st *store.Store) crypt.Crypt {
	return crypt.New(st.RecipientsPath(), config.From(ctx).IdentityPath)
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

// listCommand shows the named stores. Which one is active matters as much as
// which exist, because every other command acts on it silently.
func listCommand() *cli.Command {
	return &cli.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Usage:   "list the named stores",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			names, err := config.ListStores()
			if err != nil {
				return err
			}

			active := config.From(ctx).StoreDir
			if len(names) == 0 {
				fmt.Printf("no named stores yet — the active one is %s\n", active)
				fmt.Println("create one with 'hamzad --store work store init'")

				return nil
			}

			w := tabwriter.NewWriter(cmd.Root().Writer, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tPATH\tACTIVE")
			for _, n := range names {
				path, err := config.StorePath(n)
				if err != nil {
					continue
				}
				mark := "-"
				if path == active {
					mark = "yes"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", n, path, mark)
			}

			return w.Flush()
		},
	}
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
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "reencrypt",
						Usage: "also re-encrypt existing secrets and bundles so the new recipients can read them",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return errRecipientRequired
					}

					st := storeFrom(ctx)
					added, err := addRecipients(st, args)
					if err != nil {
						return err
					}

					fmt.Printf("added %d recipient(s)\n", added)

					if !cmd.Bool("reencrypt") {
						fmt.Println("existing secrets and bundles are still encrypted to the previous recipients —")
						fmt.Println("run 'store reencrypt' so the new ones can read them")

						return nil
					}

					return reencrypt(ctx, st)
				},
			},
		},
	}
}

func reencryptCommand() *cli.Command {
	return &cli.Command{
		Name:  "reencrypt",
		Usage: "re-encrypt every stored secret and session bundle to the current recipients",
		Description: "age fixes the recipient set when a file is written, so keys added later cannot read\n" +
			"anything already stored. This rewrites every secrets/<name>.age and data/<name>.tar.age\n" +
			"to the current recipients.txt, using your identity to read them first.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return reencrypt(ctx, storeFrom(ctx))
		},
	}
}

// reencrypt rewrites the store's artifacts to the current recipient list and
// prints one line per artifact. An artifact the local identity cannot decrypt
// is reported and skipped, and makes the command exit non-zero at the end — a
// silent skip would look exactly like a successful share.
func reencrypt(ctx context.Context, st *store.Store) error {
	results, err := st.Reencrypt(cryptFor(ctx, st))
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("nothing to re-encrypt — the store holds no secrets or session bundles")

		return nil
	}

	failed := 0
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, r := range results {
		if r.Err != nil {
			failed++
			_, _ = fmt.Fprintf(w, "  %s\t%s\tFAILED: %v\n", r.Kind, r.Name, r.Err)

			continue
		}
		_, _ = fmt.Fprintf(w, "  %s\t%s\tok\n", r.Kind, r.Name)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}

	rewritten := len(results) - failed
	fmt.Printf("re-encrypted %d of %d artifact(s) to %d recipient(s)\n", rewritten, len(results), recipientCount(st))
	if rewritten > 0 {
		fmt.Println("run 'store sync' to share them — every rewritten bundle is committed whole")
	}

	if failed > 0 {
		return fmt.Errorf("%w: %d artifact(s) your identity could not decrypt", errReencryptFailed, failed)
	}

	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}

	return s
}
