// Package serve provides the "serve" command: the local automation API.
package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/code-chorus-io/hamzad/internal/infra/api"
	"github.com/code-chorus-io/hamzad/internal/infra/chrome"
	"github.com/code-chorus-io/hamzad/internal/infra/config"
	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
	"github.com/code-chorus-io/hamzad/internal/infra/manager"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// DefaultListen is the API's default address.
const DefaultListen = "127.0.0.1:36912"

// tokenEnv carries a fixed token, so scripts need not scrape the startup line.
const tokenEnv = config.EnvPrefix + "SERVE_TOKEN"

// errNotLoopback is returned for a listen address off this machine.
var errNotLoopback = errors.New("serve listens on loopback only (127.0.0.1, ::1 or localhost): " +
	"it hands out control of signed-in browsers")

// errNoPort is returned when the kernel hands back a non-TCP address.
var errNoPort = errors.New("finding a free port: unexpected address")

// shutdownTimeout bounds the HTTP server's graceful stop.
const shutdownTimeout = 5 * time.Second

// Command returns the "serve" command.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "run the local automation API: start and stop profiles over HTTP, get their DevTools endpoints",
		Description: "Every request needs 'Authorization: Bearer <token>'. The token is " + tokenEnv +
			" or --token, or a random one printed at startup. Sessions started here always have a DevTools port, " +
			"and they are saved like any other when stopped. Stopping the server stops every browser it started.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "listen", Value: DefaultListen, Usage: "loopback address to listen on"},
			&cli.StringFlag{Name: "token", Usage: "bearer token to require", Sources: cli.EnvVars(tokenEnv)},
		},
		Action: run,
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	addr := cmd.String("listen")
	if err := checkLoopback(addr); err != nil {
		return err
	}

	token := cmd.String("token")
	if token == "" {
		var err error
		if token, err = newToken(); err != nil {
			return err
		}
	}

	st, mgr, err := newManager(ctx)
	if err != nil {
		return err
	}
	mgr.OnEvent(logEvent)
	defer mgr.CloseAll()

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler: api.New(api.Options{
			Sessions:     mgr,
			Profiles:     st,
			Token:        token,
			FreePort:     func() (int, error) { return freePort(ctx) },
			WaitEndpoint: chrome.WaitForEndpoint,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("hamzad API on http://%s\n", listener.Addr())
	if !cmd.IsSet("token") {
		fmt.Printf("token: %s\n", token)
	}
	fmt.Println("Ctrl-C stops the server and every browser it started")

	return serveUntilSignal(ctx, srv, listener)
}

// serveUntilSignal serves until Ctrl-C or SIGTERM, then stops the HTTP server.
// The caller's deferred CloseAll then stops and saves every browser.
func serveUntilSignal(ctx context.Context, srv *http.Server, listener net.Listener) error {
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serving: %w", err)
	case <-sigCtx.Done():
	}

	fmt.Println("stopping — saving open sessions…")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	return nil
}

// newManager opens the configured store and builds the session manager over it.
func newManager(ctx context.Context) (*store.Store, *manager.Manager, error) {
	cfg := config.From(ctx)
	st := store.New(cfg.StoreDir)
	c := cryptFor(cfg, st)

	execPath, err := chrome.Resolve(cfg.ChromePath, cfg.ChromeVersion)
	if err != nil {
		return nil, nil, err
	}

	// Ask for an SSH key passphrase now, at the terminal, rather than in the
	// middle of the first API call that needs to decrypt something.
	if err := unlock(st, c); err != nil {
		return nil, nil, err
	}

	return st, manager.New(st, c, execPath), nil
}

// checkLoopback refuses an address that is not on this machine.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errNotLoopback
	}

	return nil
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}

	return hex.EncodeToString(b), nil
}

func cryptFor(cfg *config.Config, st *store.Store) crypt.Crypt {
	c := crypt.New(st.RecipientsPath(), cfg.IdentityPath)
	c.Prompt = crypt.TerminalPrompt(cfg.IdentityPath, cfg.Askpass)
	if cfg.Keychain {
		c.Prompt = crypt.SystemKeychainPrompt(cfg.IdentityPath, c.Prompt)
	}

	return c
}

// unlock decrypts one stored proxy, if there is any, so a passphrase-protected
// identity is asked for once, up front; the prompt remembers the answer.
func unlock(st *store.Store, c crypt.Crypt) error {
	profiles, err := st.List()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if st.HasSecret(p.Name) {
			if _, err := st.Proxy(c, p.Name); err != nil {
				return fmt.Errorf("unlocking the store: %w", err)
			}

			return nil
		}
	}

	return nil
}

// freePort asks the kernel for an unused loopback port.
func freePort(ctx context.Context) (int, error) {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free port: %w", err)
	}
	defer func() { _ = l.Close() }()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("%w: %v", errNoPort, l.Addr())
	}

	return addr.Port, nil
}

// logEvent prints session changes, so the terminal running serve shows what
// the API is doing.
func logEvent(s manager.Session) {
	line := fmt.Sprintf("%s  %-8s %s", time.Now().Format(time.TimeOnly), s.Status, s.Name)
	if s.DebugPort > 0 && s.Status == manager.StatusRunning {
		line += fmt.Sprintf("  cdp :%d", s.DebugPort)
	}
	if s.Note != "" {
		line += "  (" + s.Note + ")"
	}
	if s.Err != nil {
		line += "  error: " + s.Err.Error()
	}
	fmt.Println(line)
}
