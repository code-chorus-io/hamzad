package profile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/code-chorus-io/hamzad/internal/infra/chrome"
	"github.com/code-chorus-io/hamzad/internal/infra/cookiefmt"
	"github.com/code-chorus-io/hamzad/internal/infra/session"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// errNoSessionData is returned when a profile has neither a working copy nor a
// bundle to read cookies from.
var errNoSessionData = errors.New("no browsing data")

// errBundleHasNoCookies is returned when a bundle predates cookie export.
var errBundleHasNoCookies = errors.New("the shared bundle has no cookie export")

// errNoBrowserForCookies is returned when no browser is available to read or
// write a working copy's cookies.
var errNoBrowserForCookies = errors.New("cookies are read and written through the browser, and none was found")

// errFileRequired is returned when import is given no file.
var errFileRequired = errors.New("a cookie file is required (or - for standard input)")

// stdio is the file name that means standard input or output.
const stdio = "-"

func cookiesCommand() *cli.Command {
	return &cli.Command{
		Name:  "cookies",
		Usage: "import or export a profile's cookies (Cookie-Editor/GoLogin JSON, Puppeteer/Playwright JSON, cookies.txt)",
		Commands: []*cli.Command{
			cookiesExportCommand(),
			cookiesImportCommand(),
		},
	}
}

func cookiesExportCommand() *cli.Command {
	return &cli.Command{
		Name:      "export",
		Usage:     "write a profile's cookies to a file or standard output, in plaintext",
		ArgsUsage: argName,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"f"},
				Value:   string(cookiefmt.FormatJSON),
				Usage:   "json (Cookie-Editor/GoLogin), cdp (Puppeteer/Playwright) or netscape (cookies.txt)",
			},
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: stdio, Usage: "file to write, or - for standard output"},
		},
		Action: cookiesExportAction,
	}
}

func cookiesExportAction(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return errNameRequired
	}

	st := storeFrom(ctx)
	if _, err := st.Get(name); err != nil {
		return err
	}

	cookies, err := profileCookies(ctx, st, name)
	if err != nil {
		return err
	}

	out, err := cookiefmt.Write(cookies, cookiefmt.Format(cmd.String("format")))
	if err != nil {
		return err
	}

	dst := cmd.String("out")
	if dst == stdio {
		if _, err := os.Stdout.Write(out); err != nil {
			return fmt.Errorf("writing cookies: %w", err)
		}

		return nil
	}

	// Owner-only: the file is as good as the logins it holds.
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		return fmt.Errorf("writing cookies: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d cookies to %s — it holds live logins in plaintext, so delete it when done\n",
		len(cookies), dst)

	return nil
}

// profileCookies reads a profile's cookies: from its working copy through the
// browser when it has one, else from the shared bundle's export, which needs
// no browser and leaves nothing on disk.
func profileCookies(ctx context.Context, st *store.Store, name string) ([]chrome.Cookie, error) {
	if dirExists(st.UserDataDir(name)) {
		execPath := browserPath(ctx)
		if execPath == "" {
			return nil, errNoBrowserForCookies
		}

		cookies, err := chrome.ReadCookies(ctx, execPath, st.UserDataDir(name))
		if err != nil {
			return nil, fmt.Errorf("reading cookies of %q (close its browser first): %w", name, err)
		}

		return cookies, nil
	}

	if !st.HasBundle(name) {
		return nil, fmt.Errorf("%w for %q: open it once, or run 'store sync' to fetch a shared bundle", errNoSessionData, name)
	}

	data, err := st.BundleCookies(cryptFor(ctx, st), name)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("%w for %q: run 'profile pull %s', then export again", errBundleHasNoCookies, name, name)
	}

	return chrome.DecodeCookies(data)
}

func cookiesImportCommand() *cli.Command {
	return &cli.Command{
		Name:      "import",
		Usage:     "add cookies from a file (or - for standard input) to a profile; the format is detected",
		ArgsUsage: argName + " <file>",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "replace", Usage: "delete the profile's existing cookies first, instead of adding to them"},
		},
		Action: cookiesImportAction,
	}
}

func cookiesImportAction(ctx context.Context, cmd *cli.Command) error {
	name, src := cmd.Args().Get(0), cmd.Args().Get(1)
	if name == "" {
		return errNameRequired
	}
	if src == "" {
		return errFileRequired
	}

	st := storeFrom(ctx)
	if _, err := st.Get(name); err != nil {
		return err
	}

	data, err := readSource(src)
	if err != nil {
		return err
	}
	cookies, err := cookiefmt.Read(data)
	if err != nil {
		return err
	}

	execPath := browserPath(ctx)
	if execPath == "" {
		return errNoBrowserForCookies
	}

	if err := ensureWorkingCopy(ctx, st, execPath, name); err != nil {
		return err
	}

	written, refused, err := chrome.WriteCookies(ctx, execPath, st.UserDataDir(name), cookies, cmd.Bool("replace"))
	if err != nil {
		return fmt.Errorf("writing cookies to %q (close its browser first): %w", name, err)
	}

	fmt.Printf("imported %d cookies into %q", written, name)
	if refused > 0 {
		fmt.Printf(" (%d refused by the browser)", refused)
	}
	fmt.Printf(" — run 'profile push %s' to share them\n", name)

	return nil
}

// ensureWorkingCopy restores the shared bundle when the profile has one but no
// working copy yet. A first working copy must come from the bundle, not from
// nothing: once a working copy exists, open keeps it and never restores the
// bundle, so importing into an empty directory would hide the shared session
// for good.
func ensureWorkingCopy(ctx context.Context, st *store.Store, execPath, name string) error {
	if dirExists(st.UserDataDir(name)) || !st.HasBundle(name) {
		return nil
	}

	fmt.Printf("restoring encrypted session for %q first…\n", name)
	res, err := session.Restore(ctx, st, cryptFor(ctx, st), execPath, name)
	if err != nil {
		return err
	}
	reportRestore(res)

	return nil
}

// readSource reads a file, or standard input for "-".
func readSource(src string) ([]byte, error) {
	if src == stdio {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading standard input: %w", err)
		}

		return data, nil
	}

	data, err := os.ReadFile(strings.TrimSpace(src))
	if err != nil {
		return nil, fmt.Errorf("reading cookie file: %w", err)
	}

	return data, nil
}
