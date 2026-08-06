package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/urfave/cli/v3"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
	"github.com/1995parham/koochooloologin/internal/infra/chrome"
	"github.com/1995parham/koochooloologin/internal/infra/config"
	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/geo"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

func openCommand() *cli.Command {
	return &cli.Command{
		Name:      "open",
		Aliases:   []string{"launch", "run"},
		Usage:     "launch a profile's browser",
		ArgsUsage: argName,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "auto-geo", Usage: "derive timezone & geolocation from the proxy's exit IP for this session"},
			&cli.StringFlag{Name: "start-url", Usage: "override the profile's start URL"},
			&cli.IntFlag{Name: "cdp-port", Usage: "fix the remote-debugging port so automation tools can attach"},
			&cli.BoolFlag{Name: "no-restore", Usage: "do not decrypt the shared session bundle before launch"},
			&cli.BoolFlag{Name: "no-save", Usage: "do not re-encrypt the session bundle after the browser closes"},
		},
		Action: openAction,
	}
}

func openAction(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return errNameRequired
	}

	st := storeFrom(ctx)
	c := cryptFor(ctx, st)

	opts, err := openOptions(ctx, cmd, st, c, name)
	if err != nil {
		return err
	}

	if err := restoreSession(st, c, name, cmd.Bool("no-restore")); err != nil {
		return err
	}

	// Tie the browser lifetime to Ctrl-C as well as window close.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	if opts.DebugPort > 0 {
		go announceCDP(ctx, opts.DebugPort)
	} else if dropped := chrome.CleanModeDropped(opts); dropped != "" {
		fmt.Printf("note: %s not applied on the clean (no-CDP) launch — use --cdp-port to enable them\n", dropped)
	}

	fmt.Printf("launching %q — close the browser window or press Ctrl-C to end the session\n", name)
	if err := chrome.Launch(ctx, opts); err != nil {
		return err
	}

	return saveSession(cmd, st, c, name)
}

// restoreSession decrypts the shared bundle over the working directory, unless
// disabled, absent, or outvoted by a local working copy.
func restoreSession(st *store.Store, c crypt.Crypt, name string, disabled bool) error {
	if disabled || !st.HasBundle(name) {
		return nil
	}

	// Local data wins: unpacking replaces the working directory outright, and it
	// may hold browsing the bundle predates. Say so out loud, though — otherwise
	// a teammate who has opened this profile before pulls a newer shared session
	// and watches `open` silently ignore it.
	if dirExists(st.UserDataDir(name)) {
		fmt.Printf("note: keeping the local session for %q — the shared bundle was not restored "+
			"(run 'profile pull %s' to replace the local copy with it)\n", name, name)

		return nil
	}

	fmt.Printf("restoring encrypted session for %q…\n", name)

	return st.UnpackBundle(c, name)
}

// saveSession re-encrypts the working data into the shared bundle after a
// session ends, unless disabled or encryption is not configured.
func saveSession(cmd *cli.Command, st *store.Store, c crypt.Crypt, name string) error {
	if cmd.Bool("no-save") || !c.Configured() {
		return nil
	}

	fmt.Printf("saving encrypted session for %q…\n", name)
	if err := st.PackBundle(c, name); err != nil {
		return err
	}

	fmt.Println("session saved — run 'store sync' to share it")

	return nil
}

// openOptions resolves a profile into launch options, decrypting its proxy and
// applying the start-url override and optional auto-geo for this session.
func openOptions(ctx context.Context, cmd *cli.Command, st *store.Store, c crypt.Crypt, name string) (chrome.Options, error) {
	p, err := st.Get(name)
	if err != nil {
		return chrome.Options{}, err
	}

	cfg := config.From(ctx)
	execPath, err := chrome.Resolve(cfg.ChromePath, cfg.ChromeVersion)
	if err != nil {
		return chrome.Options{}, err
	}

	opts := chrome.Options{
		ExecPath:    execPath,
		UserDataDir: st.UserDataDir(name),
		Timezone:    p.Timezone,
		StartURL:    p.StartURL,
		DebugPort:   cmd.Int("cdp-port"),
		Fingerprint: p.Fingerprint,
	}
	if u := cmd.String("start-url"); u != "" {
		opts.StartURL = u
	}

	proxyURL, err := resolveProxy(st, c, name)
	if err != nil {
		return chrome.Options{}, err
	}
	opts.Proxy = proxyURL

	if cmd.Bool("auto-geo") {
		if err := applyAutoGeo(ctx, &opts, proxyURL); err != nil {
			return chrome.Options{}, err
		}
	}

	return opts, nil
}

// resolveProxy decrypts and parses a profile's proxy, or returns nil when the
// profile has none.
func resolveProxy(st *store.Store, c crypt.Crypt, name string) (*url.URL, error) {
	if !st.HasSecret(name) {
		//nolint:nilnil // a nil URL with nil error means "this profile has no proxy"
		return nil, nil
	}

	raw, err := st.Proxy(c, name)
	if err != nil {
		return nil, err
	}

	u, err := domain.ParseProxy(raw)
	if err != nil {
		return nil, err
	}

	return u, nil
}

// applyAutoGeo fills the session timezone and geolocation from the proxy's exit
// IP, leaving any values the profile already pins untouched.
func applyAutoGeo(ctx context.Context, opts *chrome.Options, proxyURL *url.URL) error {
	loc, err := geo.Lookup(ctx, proxyURL)
	if err != nil {
		return err
	}

	fmt.Printf("auto-geo: exit IP %s (%s) → %s\n", loc.IP, loc.CountryCode, loc.Timezone)

	if opts.Timezone == "" {
		opts.Timezone = loc.Timezone
	}
	if opts.Fingerprint.Geolocation == nil {
		opts.Fingerprint.Geolocation = &domain.LatLng{Latitude: loc.Latitude, Longitude: loc.Longitude}
	}

	return nil
}

// announceCDP waits for the debugging endpoint to come up, then prints the
// WebSocket URL an external automation client can connect to.
func announceCDP(ctx context.Context, port int) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: 2 * time.Second}

	for range 30 {
		select {
		case <-ctx.Done():
			return
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			time.Sleep(300 * time.Millisecond)

			continue
		}

		var body struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err == nil && body.WebSocketDebuggerURL != "" {
			fmt.Printf("CDP ready — attach automation to:\n  %s\n", body.WebSocketDebuggerURL)

			return
		}

		time.Sleep(300 * time.Millisecond)
	}
}

func geoCommand() *cli.Command {
	return &cli.Command{
		Name:      "geo",
		Usage:     "resolve timezone & geolocation from the proxy IP and save them to the profile",
		ArgsUsage: argName,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return errNameRequired
			}

			st := storeFrom(ctx)
			p, err := st.Get(name)
			if err != nil {
				return err
			}

			proxyURL, err := resolveProxy(st, cryptFor(ctx, st), name)
			if err != nil {
				return err
			}

			loc, err := geo.Lookup(ctx, proxyURL)
			if err != nil {
				return err
			}

			p.Timezone = loc.Timezone
			p.Fingerprint.Geolocation = &domain.LatLng{Latitude: loc.Latitude, Longitude: loc.Longitude}
			if err := st.Put(p); err != nil {
				return err
			}

			fmt.Printf("%s: exit IP %s (%s)\n", name, loc.IP, loc.CountryCode)
			fmt.Printf("  timezone → %s\n", loc.Timezone)
			fmt.Printf("  geo      → %.5f, %.5f\n", loc.Latitude, loc.Longitude)

			return nil
		},
	}
}

func pullCommand() *cli.Command {
	return &cli.Command{
		Name:      "pull",
		Usage:     "decrypt and extract a profile's shared session bundle over the local working data",
		ArgsUsage: argName,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return errNameRequired
			}

			st := storeFrom(ctx)
			if !st.HasBundle(name) {
				return fmt.Errorf("%w for %q — run 'store sync' to fetch it first", errNoBundle, name)
			}

			if err := st.UnpackBundle(cryptFor(ctx, st), name); err != nil {
				return err
			}

			fmt.Printf("restored session for %q\n", name)

			return nil
		},
	}
}

func pushCommand() *cli.Command {
	return &cli.Command{
		Name:      "push",
		Usage:     "encrypt the local working data into the shared session bundle",
		ArgsUsage: argName,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return errNameRequired
			}

			st := storeFrom(ctx)
			c := cryptFor(ctx, st)
			if !c.Configured() {
				return errEncNotConfigured
			}

			if err := st.PackBundle(c, name); err != nil {
				return err
			}

			fmt.Printf("packed encrypted session for %q — run 'store sync' to share\n", name)

			return nil
		},
	}
}
