// Command omnilite-standalone indexes OmniLite transactions from any
// litecoind via standard RPC.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tbuuol/omnilite-standalone/internal/config"
	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
	"github.com/tbuuol/omnilite-standalone/internal/syncer"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Check {
		if !runCheck(ctx, cfg) {
			os.Exit(1)
		}
		return
	}
	if err := run(ctx, cfg); err != nil {
		slog.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func newClient(cfg *config.Config) (*rpc.Client, error) {
	user, pass, err := cfg.Credentials()
	if err != nil {
		return nil, err
	}
	return rpc.New(cfg.RPCURL(), user, pass), nil
}

func run(ctx context.Context, cfg *config.Config) error {
	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	info, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		return fmt.Errorf("node at %s: %w", cfg.RPCURL(), err)
	}
	if info.Chain != cfg.Chain.CoreChain {
		return fmt.Errorf("node runs chain %q, configured network is %q", info.Chain, cfg.Chain.Name)
	}
	if enabled, _, err := client.TxIndex(ctx); err == nil && !enabled {
		return fmt.Errorf("node runs without -txindex; restart litecoind with -txindex=1")
	}
	dec, err := omni.NewDecoder(&cfg.Chain)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath(), cfg.Chain.Name, cfg.UndoDepth)
	if err != nil {
		return err
	}
	defer st.Close()

	slog.Info("syncing", "network", cfg.Chain.Name, "node", cfg.RPCURL(), "db", cfg.DBPath(), "start", cfg.Chain.OmniGenesis)
	s := &syncer.Syncer{
		Src:   client,
		Store: st,
		Proc: &indexer.Indexer{Node: client, Dec: dec, Log: slog.Default(), State: state.Options{
			ActivationAllowSenders: cfg.ActivationAllowSenders,
			AlertAllowSenders:      cfg.AlertAllowSenders,
		}, HashEvery: cfg.ConsensusHashEvery},
		Start: cfg.Chain.OmniGenesis,
		Poll:  cfg.PollInterval,
		Log:   slog.Default(),
	}
	return s.Run(ctx)
}

// runCheck prints the node and database status and reports whether
// everything needed for indexing is in place.
func runCheck(ctx context.Context, cfg *config.Config) bool {
	ok := true
	line := func(label, format string, args ...any) {
		fmt.Printf("%-12s %s\n", label+":", fmt.Sprintf(format, args...))
	}
	fail := func(label, format string, args ...any) {
		ok = false
		line(label, "ERROR: "+format, args...)
	}

	line("RPC", "%s", cfg.RPCURL())
	client, err := newClient(cfg)
	if err != nil {
		fail("Login", "%v", err)
		return false
	}
	netInfo, err := client.GetNetworkInfo(ctx)
	if err != nil {
		fail("Node", "unreachable: %v", err)
		return false
	}
	line("Node", "%s (%d)", netInfo.Subversion, netInfo.Version)

	info, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		fail("Chain", "%v", err)
		return false
	}
	if info.Chain == cfg.Chain.CoreChain {
		line("Chain", "%s", info.Chain)
	} else {
		fail("Chain", "node runs %q, configured network is %q", info.Chain, cfg.Chain.Name)
	}
	line("Height", "%d blocks, %d headers", info.Blocks, info.Headers)
	if info.InitialBlockDownload {
		line("Sync", "node is still syncing (%.2f %%)", info.VerificationProgress*100)
	} else {
		line("Sync", "done")
	}

	enabled, synced, err := client.TxIndex(ctx)
	switch {
	case err != nil:
		fail("txindex", "cannot check: %v", err)
	case !enabled:
		fail("txindex", "off, litecoind needs -txindex=1")
	case !synced:
		line("txindex", "on, still building")
	default:
		line("txindex", "on")
	}
	if info.Pruned {
		fail("Pruning", "node is pruned, all blocks from %d are needed", cfg.Chain.OmniGenesis)
	}

	if info.Blocks >= cfg.Chain.OmniGenesis {
		line("Omni start", "block %d (reached)", cfg.Chain.OmniGenesis)
	} else {
		line("Omni start", "block %d (node is not there yet)", cfg.Chain.OmniGenesis)
	}

	st, err := store.Open(cfg.DBPath(), cfg.Chain.Name, cfg.UndoDepth)
	if err != nil {
		fail("Database", "%v", err)
		return ok
	}
	defer st.Close()
	tip, err := st.Tip(ctx)
	switch {
	case err != nil:
		fail("Database", "%v", err)
	case tip == nil:
		line("Database", "%s (empty)", cfg.DBPath())
	default:
		line("Database", "%s (block %d)", cfg.DBPath(), tip.Height)
		if h, err := state.New(st.View(ctx), &cfg.Chain, state.Options{}).ConsensusHash(); err == nil {
			line("Consensus", "%s", h)
		}
	}
	return ok
}
