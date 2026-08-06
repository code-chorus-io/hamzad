// Package profile provides the "profile" command group: create, list, inspect,
// launch, and delete browser profiles.
package profile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
	"github.com/1995parham/koochooloologin/internal/infra/config"
	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

// errNameRequired is returned by commands invoked without a profile name.
var errNameRequired = errors.New("profile name is required")

// errInvalidScreen is returned when a --screen value is not WxH.
var errInvalidScreen = errors.New("invalid screen (want WxH, e.g. 1920x1080)")

// errEncNotConfigured is returned when a secret must be stored but no
// recipients exist yet.
var errEncNotConfigured = errors.New("encryption not configured; run 'store init' to add your key first")

// errNoBundle is returned when a session bundle is expected but absent.
var errNoBundle = errors.New("no session bundle")

// argName is the shared ArgsUsage hint for commands taking a profile name.
const argName = "<name>"

// Command returns the "profile" command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:    "profile",
		Aliases: []string{"p"},
		Usage:   "manage browser profiles",
		Commands: []*cli.Command{
			addCommand(),
			listCommand(),
			showCommand(),
			openCommand(),
			geoCommand(),
			pullCommand(),
			pushCommand(),
			removeCommand(),
		},
	}
}

// storeFrom builds a store handle from the configuration carried by ctx.
func storeFrom(ctx context.Context) *store.Store {
	return store.New(config.From(ctx).StoreDir)
}

// cryptFor builds the crypt handle for a store from the configured identity.
func cryptFor(ctx context.Context, st *store.Store) crypt.Crypt {
	return crypt.New(st.RecipientsPath(), config.From(ctx).IdentityPath)
}

// fingerprintFlags are shared by add so a profile's fingerprint can be set at
// creation time.
func fingerprintFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "proxy", Usage: "proxy URL, e.g. socks5://user:pass@host:1080"},
		&cli.StringFlag{Name: "timezone", Usage: "IANA timezone, e.g. Europe/Berlin"},
		&cli.StringFlag{Name: "start-url", Usage: "page to open on launch"},
		&cli.StringFlag{Name: "notes", Usage: "free-form note attached to the profile"},
		&cli.StringFlag{Name: "user-agent", Usage: "navigator.userAgent override"},
		&cli.StringFlag{Name: "platform", Usage: "navigator.platform override"},
		&cli.StringFlag{Name: "accept-language", Usage: "Accept-Language header override"},
		&cli.StringSliceFlag{Name: "lang", Usage: "navigator.languages entry (repeatable)"},
		&cli.StringFlag{Name: "screen", Usage: "screen size as WxH, e.g. 1920x1080"},
		&cli.IntFlag{Name: "hardware-concurrency", Usage: "navigator.hardwareConcurrency"},
		&cli.IntFlag{Name: "device-memory", Usage: "navigator.deviceMemory (GB)"},
		&cli.StringFlag{Name: "webgl-vendor", Usage: "WebGL UNMASKED_VENDOR override"},
		&cli.StringFlag{Name: "webgl-renderer", Usage: "WebGL UNMASKED_RENDERER override"},
		&cli.BoolFlag{Name: "canvas-noise", Usage: "add per-profile canvas readback noise"},
		&cli.StringFlag{
			Name: "webrtc-mode",
			Usage: "WebRTC IP handling: disable_non_proxied_udp (default with a proxy), " +
				"default_public_interface_only, default_public_and_private_interfaces, default",
		},
	}
}

// applyFlags populates a profile from the flags set on cmd.
func applyFlags(p *domain.Profile, cmd *cli.Command) error {
	p.Proxy = cmd.String("proxy")
	p.Timezone = cmd.String("timezone")
	p.StartURL = cmd.String("start-url")
	p.Notes = cmd.String("notes")

	fp := &p.Fingerprint
	fp.UserAgent = cmd.String("user-agent")
	fp.Platform = cmd.String("platform")
	fp.AcceptLanguage = cmd.String("accept-language")
	fp.Languages = cmd.StringSlice("lang")
	fp.HardwareConcurrent = cmd.Int("hardware-concurrency")
	fp.DeviceMemory = cmd.Int("device-memory")
	fp.WebGLVendor = cmd.String("webgl-vendor")
	fp.WebGLRenderer = cmd.String("webgl-renderer")
	fp.CanvasNoise = cmd.Bool("canvas-noise")
	fp.WebRTCMode = cmd.String("webrtc-mode")

	if s := cmd.String("screen"); s != "" {
		w, h, err := parseScreen(s)
		if err != nil {
			return err
		}
		fp.ScreenWidth, fp.ScreenHeight = w, h
	}

	return nil
}

func parseScreen(s string) (int, int, error) {
	parts := strings.SplitN(strings.ToLower(s), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%w: %q", errInvalidScreen, s)
	}

	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid screen width in %q: %w", s, err)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid screen height in %q: %w", s, err)
	}

	return w, h, nil
}

func addCommand() *cli.Command {
	return &cli.Command{
		Name:      "add",
		Usage:     "create a new profile",
		ArgsUsage: argName,
		Flags:     fingerprintFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return errNameRequired
			}

			p := domain.Profile{Name: name}
			if err := applyFlags(&p, cmd); err != nil {
				return err
			}

			st := storeFrom(ctx)
			c := cryptFor(ctx, st)
			if p.Proxy != "" && !c.Configured() {
				return errEncNotConfigured
			}

			if err := st.Add(p); err != nil {
				return err
			}

			if p.Proxy != "" {
				if err := st.SetProxy(c, name, p.Proxy); err != nil {
					return err
				}
				fmt.Printf("created profile %q (proxy stored encrypted)\n", name)

				return nil
			}

			fmt.Printf("created profile %q\n", name)

			return nil
		},
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Usage:   "list all profiles",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st := storeFrom(ctx)
			profiles, err := st.List()
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				fmt.Println("no profiles yet — create one with 'profile add <name>'")

				return nil
			}

			w := tabwriter.NewWriter(cmd.Root().Writer, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tPROXY\tTIMEZONE\tSESSION\tNOTES")
			for _, p := range profiles {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					p.Name, proxyState(st, p.Name), orDash(p.Timezone), sessionState(st, p.Name), orDash(p.Notes))
			}

			return w.Flush()
		},
	}
}

func showCommand() *cli.Command {
	return &cli.Command{
		Name:      "show",
		Usage:     "show a profile's full configuration",
		ArgsUsage: argName,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "reveal", Usage: "decrypt and print the proxy (needs your identity)"},
		},
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

			proxyDisplay, err := showProxy(ctx, st, name, cmd.Bool("reveal"))
			if err != nil {
				return err
			}

			fmt.Printf("name:       %s\n", p.Name)
			fmt.Printf("proxy:      %s\n", proxyDisplay)
			fmt.Printf("timezone:   %s\n", orDash(p.Timezone))
			fmt.Printf("start-url:  %s\n", orDash(p.StartURL))
			fmt.Printf("notes:      %s\n", orDash(p.Notes))
			fp := p.Fingerprint
			fmt.Printf("user-agent: %s\n", orDash(fp.UserAgent))
			fmt.Printf("platform:   %s\n", orDash(fp.Platform))
			fmt.Printf("languages:  %s\n", orDash(strings.Join(fp.Languages, ", ")))
			if fp.ScreenWidth > 0 {
				fmt.Printf("screen:     %dx%d\n", fp.ScreenWidth, fp.ScreenHeight)
			}
			if fp.HardwareConcurrent > 0 {
				fmt.Printf("cpu-cores:  %d\n", fp.HardwareConcurrent)
			}
			if fp.DeviceMemory > 0 {
				fmt.Printf("memory-gb:  %d\n", fp.DeviceMemory)
			}
			if fp.WebGLVendor != "" || fp.WebGLRenderer != "" {
				fmt.Printf("webgl:      %s / %s\n", orDash(fp.WebGLVendor), orDash(fp.WebGLRenderer))
			}
			if fp.Geolocation != nil {
				fmt.Printf("geo:        %.5f, %.5f\n", fp.Geolocation.Latitude, fp.Geolocation.Longitude)
			}

			return nil
		},
	}
}

func removeCommand() *cli.Command {
	return &cli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "delete a profile",
		ArgsUsage: argName,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "purge", Usage: "also delete the profile's browsing data on disk"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			name := cmd.Args().First()
			if name == "" {
				return errNameRequired
			}

			if err := storeFrom(ctx).Remove(name, cmd.Bool("purge")); err != nil {
				return err
			}

			fmt.Printf("removed profile %q\n", name)

			return nil
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// proxyState is the list column indicating whether an encrypted proxy is set.
func proxyState(st *store.Store, name string) string {
	if st.HasSecret(name) {
		return "enc"
	}

	return "-"
}

// sessionState is the list column indicating whether a shared session bundle
// and/or a local working copy exist.
func sessionState(st *store.Store, name string) string {
	switch {
	case st.HasBundle(name):
		return "bundle"
	case dirExists(st.UserDataDir(name)):
		return "local"
	default:
		return "-"
	}
}

// showProxy renders the proxy line for `show`, decrypting only when reveal is
// set. Without reveal it never touches the identity.
func showProxy(ctx context.Context, st *store.Store, name string, reveal bool) (string, error) {
	if !st.HasSecret(name) {
		return "-", nil
	}
	if !reveal {
		return "(encrypted — use --reveal)", nil
	}

	proxy, err := st.Proxy(cryptFor(ctx, st), name)
	if err != nil {
		return "", err
	}

	return proxy, nil
}
