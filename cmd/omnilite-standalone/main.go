// Command omnilite-standalone indexes OmniLite transactions from any
// litecoind via standard RPC.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/api"
	"github.com/tbuuol/omnilite-standalone/internal/config"
	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/mempool"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
	"github.com/tbuuol/omnilite-standalone/internal/syncer"
	"github.com/tbuuol/omnilite-standalone/internal/wallet"
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

	// The mempool view decodes with its own indexer: the prevout cache is
	// not shared with the syncer's goroutine.
	pool := &mempool.Pool{Node: client, Dec: &indexer.Indexer{Node: client, Dec: dec}, Poll: cfg.PollInterval, Log: slog.Default()}
	go pool.Run(ctx)

	opts := state.Options{
		ActivationAllowSenders: cfg.ActivationAllowSenders,
		AlertAllowSenders:      cfg.AlertAllowSenders,
	}
	var wal *wallet.Wallet
	if cfg.Wallet {
		node := client.ForWallet(cfg.WalletName)
		var wi struct {
			WalletName string `json:"walletname"`
		}
		if err := node.Call(ctx, "getwalletinfo", &wi); err != nil {
			return fmt.Errorf("wallet=1, but the node's wallet is not available: %w", err)
		}
		wal = &wallet.Wallet{Node: node, Dec: &indexer.Indexer{Node: client, Dec: dec}, Store: st, Pool: pool,
			Params: &cfg.Chain, State: opts, FallbackFee: cfg.FallbackFee, Log: slog.Default()}
		slog.Info("sending enabled", "wallet", wi.WalletName)
		// Watches open DEx accepts of the wallet; it never pays.
		go wal.Watch(ctx, cfg.PollInterval)
		if !cfg.API {
			slog.Warn("wallet=1 has no effect with api=0")
		}
	}

	if cfg.API {
		if err := startAPI(ctx, cfg, client, st, pool, wal); err != nil {
			return err
		}
	}

	slog.Info("syncing", "network", cfg.Chain.Name, "node", cfg.RPCURL(), "db", cfg.DBPath(), "start", cfg.Chain.OmniGenesis)
	s := &syncer.Syncer{
		Src:   client,
		Store: st,
		Proc: &indexer.Indexer{Node: client, Dec: dec, Log: slog.Default(), State: opts,
			HashEvery: cfg.ConsensusHashEvery, Confirmed: pool.Confirmed},
		Start: cfg.Chain.OmniGenesis,
		Poll:  cfg.PollInterval,
		Log:   slog.Default(),
	}
	return s.Run(ctx)
}

// startAPI serves the JSON-RPC API until ctx ends.
func startAPI(ctx context.Context, cfg *config.Config, client *rpc.Client, st *store.Store, pool *mempool.Pool, wal *wallet.Wallet) error {
	user, pass := cfg.APIUser, cfg.APIPassword
	if pass == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		user, pass = "__cookie__", hex.EncodeToString(b)
		if err := os.WriteFile(cfg.APICookieFile(), []byte(user+":"+pass), 0o600); err != nil {
			return fmt.Errorf("write api cookie: %w", err)
		}
	}
	if info, err := client.GetNetworkInfo(ctx); err == nil {
		v := info.Version
		api.NodeVersion = fmt.Sprintf("%d.%d.%d", v/1000000, v/10000%100, v/100%100)
		if v%100 != 0 {
			api.NodeVersion += fmt.Sprintf(".%d", v%100)
		}
	}

	addr := net.JoinHostPort(cfg.APIBind, strconv.Itoa(cfg.APIPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	srv := &http.Server{
		Handler:           &api.Server{Store: st, Params: &cfg.Chain, Node: client, Pool: pool, Wallet: wal, User: user, Pass: pass, Log: slog.Default()},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api stopped", "err", err)
		}
	}()
	slog.Info("api listening", "addr", addr, "auth", map[bool]string{true: "cookie " + cfg.APICookieFile(), false: "apiuser/apipassword"}[cfg.APIPassword == ""])
	return nil
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
