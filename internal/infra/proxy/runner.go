package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
)

// inboundTag names the loopback listener Chrome connects to.
const inboundTag = "chrome"

// loopback is the only interface the relay ever binds: the local HTTP listener
// is unauthenticated, so it must not be reachable from off the machine.
const loopback = "127.0.0.1"

// Relay is a running sing-box instance fronting one profile's upstream proxy
// with a local, unauthenticated HTTP listener.
type Relay struct {
	instance *box.Box
	addr     string
}

// Addr is the host:port to hand Chrome via --proxy-server.
func (r *Relay) Addr() string { return r.addr }

// Close stops the instance and releases the port.
func (r *Relay) Close() error {
	if r.instance == nil {
		return nil
	}
	if err := r.instance.Close(); err != nil {
		return fmt.Errorf("stopping proxy relay: %w", err)
	}

	return nil
}

// startAttempts bounds the retry below. Each attempt draws a fresh port, so
// losing the race twice running is already remote; more attempts would only
// delay reporting a port exhaustion that is not going to clear on its own.
const startAttempts = 3

// Start brings up a sing-box instance that listens for HTTP on loopback and
// forwards through the given outbound.
//
// The port is chosen by binding one and releasing it, rather than asking
// sing-box for an ephemeral port: its config takes a fixed listen_port, and the
// caller needs the number to build Chrome's --proxy-server argument before the
// browser starts.
//
// That leaves a window between the release and sing-box's bind in which anything
// on the machine can take the port — including a second profile opening at the
// same moment, which is a thing this tool exists to do. Losing that race is
// retried with a fresh port; every other failure is returned as-is, so a bad
// outbound still fails on the first attempt.
func Start(ctx context.Context, out Outbound) (*Relay, error) {
	return startWithRetry(ctx, out, start)
}

// startWithRetry runs attempt until it succeeds, fails for a reason other than
// a taken port, or runs out of tries. attempt is a parameter so the retry can be
// tested without racing a real listener into the window it exists to cover.
func startWithRetry(
	ctx context.Context,
	out Outbound,
	attempt func(context.Context, Outbound) (*Relay, error),
) (*Relay, error) {
	var lost error
	for range startAttempts {
		relay, err := attempt(ctx, out)
		if err == nil {
			return relay, nil
		}
		if !isAddrInUse(err) {
			return nil, err
		}
		lost = err
	}

	return nil, fmt.Errorf("%w after %d attempts: %w", errPortRace, startAttempts, lost)
}

// isAddrInUse reports whether err is the kernel refusing a bind because the
// port is taken. sing-box wraps the failure several layers deep, but every
// layer unwraps, so the syscall errno is still reachable.
func isAddrInUse(err error) bool {
	return errors.Is(err, errAddrInUse)
}

// start makes one attempt at bringing the relay up on a freshly chosen port.
func start(ctx context.Context, out Outbound) (*Relay, error) {
	port, err := freePort(ctx)
	if err != nil {
		return nil, err
	}

	raw, err := buildConfig(out, port)
	if err != nil {
		return nil, err
	}

	boxCtx := registryContext(ctx)

	options, err := sjson.UnmarshalExtendedContext[option.Options](boxCtx, raw)
	if err != nil {
		return nil, fmt.Errorf("building proxy config: %w", err)
	}

	instance, err := box.New(box.Options{Context: boxCtx, Options: options})
	if err != nil {
		return nil, fmt.Errorf("creating proxy relay: %w", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()

		return nil, fmt.Errorf("starting proxy relay: %w", err)
	}

	return &Relay{instance: instance, addr: net.JoinHostPort(loopback, strconv.Itoa(port))}, nil
}

// buildConfig renders the one-inbound/one-outbound configuration as JSON.
func buildConfig(out Outbound, port int) ([]byte, error) {
	cfg := map[string]any{
		// sing-box logs to stderr by default, which would scribble over the TUI.
		"log": map[string]any{"disabled": true},
		"inbounds": []any{
			map[string]any{
				keyType:       "mixed",
				"tag":         inboundTag,
				"listen":      loopback,
				"listen_port": port,
			},
		},
		"outbounds": []any{map[string]any(out)},
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encoding proxy config: %w", err)
	}

	return raw, nil
}

// freePort asks the kernel for an unused loopback port.
func freePort(ctx context.Context) (int, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", loopback+":0")
	if err != nil {
		return 0, fmt.Errorf("reserving proxy port: %w", err)
	}
	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserving proxy port: %w", errNotTCP)
	}

	return addr.Port, nil
}

// errNotTCP is returned when the reserved listener is somehow not TCP.
var errNotTCP = errors.New("reserved listener is not TCP")

// errPortRace is returned when every attempt lost the port to another binder,
// which points at something claiming loopback ports faster than we can use them
// rather than at a transient collision.
var errPortRace = errors.New("could not hold a loopback port for the proxy relay")

// registryMu serializes include.Context.
//
// include.Context builds sing-box's protocol registries, and on the way it
// assigns package-level function variables — naive.ConfigureHTTP3ListenerFunc
// among them — with no synchronization of its own. Two concurrent calls race,
// which the TUI would hit routinely: opening several profiles at once means
// several relays starting at once. Serializing the call is enough, because the
// writes are idempotent; it is only the overlap that is unsafe.
var registryMu sync.Mutex

// registryContext returns ctx carrying sing-box's protocol registries. Without
// them sing-box cannot resolve an outbound "type" at all.
func registryContext(ctx context.Context) context.Context {
	registryMu.Lock()
	defer registryMu.Unlock()

	return include.Context(ctx)
}

// WithRelay runs fn against a temporary relay for spec, then tears it down.
//
// It exists for the geolocation lookup, which has to egress through the very
// proxy it is measuring. Borrowing the same relay Chrome would use means the
// lookup speaks every protocol sing-box does — previously it went through
// net/http's own proxy support, which handles only http and socks5 and could
// not measure a vless or hysteria2 exit at all.
//
// A spec of "" calls fn with a nil URL, meaning a direct connection.
func WithRelay(ctx context.Context, spec string, fn func(proxyURL *url.URL) error) error {
	if strings.TrimSpace(spec) == "" {
		return fn(nil)
	}

	outbound, err := ParseSpec(spec)
	if err != nil {
		return err
	}

	relay, err := Start(ctx, outbound)
	if err != nil {
		return err
	}
	defer func() { _ = relay.Close() }()

	local, err := url.Parse("http://" + relay.Addr())
	if err != nil {
		return fmt.Errorf("addressing local relay: %w", err)
	}

	return fn(local)
}
