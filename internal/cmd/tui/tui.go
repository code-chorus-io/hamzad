// Package tui provides the "tui" command: a long-running, interactive dashboard
// that supervises many browser sessions at once. Unlike `profile open`, which
// launches one browser and blocks until it closes, the TUI keeps running while
// profiles are opened and closed, listing their live status, and lets new
// profiles be defined interactively.
package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/urfave/cli/v3"

	"github.com/1995parham/koochooloologin/internal/infra/chrome"
	"github.com/1995parham/koochooloologin/internal/infra/config"
	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/manager"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

// Command returns the "tui" command.
func Command() *cli.Command {
	return &cli.Command{
		Name:    "tui",
		Aliases: []string{"ui", "dashboard"},
		Usage:   "interactive dashboard: open, close, and watch many profiles at once",
		Action:  run,
	}
}

func run(ctx context.Context, _ *cli.Command) error {
	cfg := config.From(ctx)
	st := store.New(cfg.StoreDir)

	// Route encrypted-SSH-key passphrase prompts through a modal on the Bubble
	// Tea loop; the plain terminal fallback would fight the alt-screen program.
	unlock := newPassphraseBridge(cfg.IdentityPath)
	c := crypt.New(st.RecipientsPath(), cfg.IdentityPath)
	c.Prompt = unlock.prompt

	execPath := cfg.ChromePath
	if execPath == "" {
		execPath = chrome.Detect()
	}

	mgr := manager.New(st, c, execPath)

	model, err := newModel(ctx, st, c, mgr)
	if err != nil {
		return err
	}

	prog := tea.NewProgram(model, tea.WithContext(ctx))
	unlock.attach(prog)

	// Bridge manager events (fired from session goroutines) onto the Bubble Tea
	// message loop so the model is only ever mutated on the UI goroutine.
	mgr.OnEvent(func(s manager.Session) {
		prog.Send(sessionEventMsg{session: s})
	})

	// Close every browser and flush session bundles before the process exits,
	// however the program ends.
	defer mgr.CloseAll()

	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("running tui: %w", err)
	}

	return nil
}
