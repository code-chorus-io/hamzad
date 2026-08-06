package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
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

// Start brings up a sing-box instance that listens for HTTP on loopback and
// forwards through the given outbound.
//
// The port is chosen by binding one and releasing it, rather than asking
// sing-box for an ephemeral port: its config takes a fixed listen_port, and the
// caller needs the number to build Chrome's --proxy-server argument before the
// browser starts. The race between release and rebind is a loopback-only,
// same-process window, which is why it is acceptable here.
func Start(ctx context.Context, out Outbound) (*Relay, error) {
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
