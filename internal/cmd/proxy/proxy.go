// Package proxy provides the "proxy" command group: proxy checks run without a
// browser.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	domain "github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/config"
	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
	"github.com/code-chorus-io/hamzad/internal/infra/netcheck"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// ErrCheckFailed is returned when at least one proxy failed its check, so a
// script can tell from the exit status alone.
var ErrCheckFailed = errors.New("proxy check failed")

// errNothingToCheck is returned when no profile has a proxy and none was named.
var errNothingToCheck = errors.New("no profile has a proxy — name one, or pass --spec to test a link")

// parallelChecks is how many proxies are checked at once.
const parallelChecks = 4

// Command returns the "proxy" command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "proxy",
		Usage: "test profile proxies without opening a browser",
		Commands: []*cli.Command{
			checkCommand(),
		},
	}
}

func checkCommand() *cli.Command {
	return &cli.Command{
		Name:      "check",
		Usage:     "connect through each proxy: exit IP, location, latency, and whether the profile agrees with them",
		ArgsUsage: "[<name>...]",
		Description: "With no names, every profile that has a proxy is checked. A profile is checked against its " +
			"own settings too: a timezone or geolocation that disagrees with the exit IP is a mismatch a site " +
			"can see. The exit location comes from ipwho.is, asked through the proxy over HTTPS.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "spec", Usage: "test this share link or sing-box outbound instead of a profile"},
			&cli.BoolFlag{Name: "json", Usage: "print the results as JSON"},
		},
		Action: checkAction,
	}
}

// target is one thing to check.
type target struct {
	name    string
	spec    string
	profile *domain.Profile
}

// report is one check's outcome, as printed.
type report struct {
	Name       string   `json:"name"`
	OK         bool     `json:"ok"`
	Error      string   `json:"error,omitempty"`
	Exit       *exit    `json:"exit,omitempty"`
	ConnectMS  int64    `json:"connect_ms,omitempty"`
	RTTMS      int64    `json:"rtt_ms,omitempty"`
	Mismatches []string `json:"mismatches,omitempty"`
}

// exit is where a proxy's traffic comes out.
type exit struct {
	IP        string  `json:"ip"`
	Country   string  `json:"country"`
	Timezone  string  `json:"timezone"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func checkAction(ctx context.Context, cmd *cli.Command) error {
	targets, err := checkTargets(ctx, cmd)
	if err != nil {
		return err
	}

	reports := runChecks(ctx, targets)

	out := cmd.Root().Writer
	if cmd.Bool("json") {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return fmt.Errorf("encoding results: %w", err)
		}
	} else if err := printReports(out, reports); err != nil {
		return err
	}

	for _, r := range reports {
		if !r.OK {
			return ErrCheckFailed
		}
	}

	return nil
}

// checkTargets resolves what to check: --spec, the named profiles, or every
// profile with a proxy.
func checkTargets(ctx context.Context, cmd *cli.Command) ([]target, error) {
	if spec := cmd.String("spec"); spec != "" {
		return []target{{name: "--spec", spec: spec}}, nil
	}

	cfg := config.From(ctx)
	st := store.New(cfg.StoreDir)

	profiles, err := selectProfiles(st, cmd.Args().Slice())
	if err != nil {
		return nil, err
	}

	c := cryptFor(cfg, st)
	targets := make([]target, 0, len(profiles))
	for i := range profiles {
		p := &profiles[i]
		spec := ""
		if st.HasSecret(p.Name) {
			s, err := st.Proxy(c, p.Name)
			if err != nil {
				return nil, err
			}
			spec = s
		}
		targets = append(targets, target{name: p.Name, spec: spec, profile: p})
	}

	return targets, nil
}

// selectProfiles returns the named profiles, or with no names every profile
// that has a proxy.
func selectProfiles(st *store.Store, names []string) ([]domain.Profile, error) {
	if len(names) > 0 {
		profiles := make([]domain.Profile, 0, len(names))
		for _, name := range names {
			p, err := st.Get(name)
			if err != nil {
				return nil, err
			}
			profiles = append(profiles, p)
		}

		return profiles, nil
	}

	all, err := st.List()
	if err != nil {
		return nil, err
	}
	var profiles []domain.Profile
	for _, p := range all {
		if st.HasSecret(p.Name) {
			profiles = append(profiles, p)
		}
	}
	if len(profiles) == 0 {
		return nil, errNothingToCheck
	}

	return profiles, nil
}

// cryptFor builds the crypt handle for a store from the configured identity.
// The proxies are all decrypted before any check starts, so the passphrase is
// asked at most once and never in the middle of the output.
func cryptFor(cfg *config.Config, st *store.Store) crypt.Crypt {
	c := crypt.New(st.RecipientsPath(), cfg.IdentityPath)
	c.Prompt = crypt.TerminalPrompt(cfg.IdentityPath, cfg.Askpass)
	if cfg.Keychain {
		c.Prompt = crypt.SystemKeychainPrompt(cfg.IdentityPath, c.Prompt)
	}

	return c
}

// runChecks checks every target, parallelChecks at a time, keeping their order.
func runChecks(ctx context.Context, targets []target) []report {
	reports := make([]report, len(targets))
	sem := make(chan struct{}, parallelChecks)

	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			reports[i] = checkOne(ctx, t)
		})
	}
	wg.Wait()

	return reports
}

func checkOne(ctx context.Context, t target) report {
	r := report{Name: t.name}

	res, err := netcheck.Check(ctx, t.spec)
	if err != nil {
		r.Error = err.Error()

		return r
	}

	r.OK = true
	r.Exit = &exit{
		IP: res.Exit.IP, Country: res.Exit.CountryCode, Timezone: res.Exit.Timezone,
		Latitude: res.Exit.Latitude, Longitude: res.Exit.Longitude,
	}
	r.ConnectMS, r.RTTMS = res.Connect.Milliseconds(), res.RTT.Milliseconds()
	if t.profile != nil {
		r.Mismatches = netcheck.Mismatches(*t.profile, res.Exit, time.Now())
	}

	return r
}

func printReports(out io.Writer, reports []report) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tEXIT IP\tCOUNTRY\tTIMEZONE\tCONNECT\tRTT")
	for _, r := range reports {
		if !r.OK {
			_, _ = fmt.Fprintf(w, "%s\tFAIL\t-\t-\t-\t-\t-\n", r.Name)

			continue
		}
		status := "ok"
		if len(r.Mismatches) > 0 {
			status = "mismatch"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d ms\t%d ms\n", r.Name, status,
			r.Exit.IP, r.Exit.Country, r.Exit.Timezone, r.ConnectMS, r.RTTMS)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing results: %w", err)
	}

	for _, r := range reports {
		if r.Error != "" {
			_, _ = fmt.Fprintf(out, "\n%s: %s\n", r.Name, r.Error)
		}
		for _, m := range r.Mismatches {
			_, _ = fmt.Fprintf(out, "\n%s: %s", r.Name, m)
		}
	}
	if hasNotes(reports) {
		_, _ = fmt.Fprintln(out)
	}

	return nil
}

func hasNotes(reports []report) bool {
	for _, r := range reports {
		if len(r.Mismatches) > 0 {
			return true
		}
	}

	return false
}
