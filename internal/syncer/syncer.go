// Package syncer follows the node's best chain block by block, handing each
// block to a Processor and disconnecting blocks again on reorgs.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// Source is the part of the node RPC the syncer needs.
type Source interface {
	GetBlockCount(ctx context.Context) (int64, error)
	GetBlockHash(ctx context.Context, height int64) (string, error)
	GetBlockHeader(ctx context.Context, hash string) (*rpc.BlockHeader, error)
}

// Processor applies one block to the state. All writes go through tx, so
// they are undone automatically when the block is disconnected.
type Processor interface {
	ConnectBlock(ctx context.Context, tx *store.BlockTx, hdr *rpc.BlockHeader) error
}

// NopProcessor only records the blocks themselves.
type NopProcessor struct{}

func (NopProcessor) ConnectBlock(context.Context, *store.BlockTx, *rpc.BlockHeader) error { return nil }

type Syncer struct {
	Src   Source
	Store *store.Store
	Proc  Processor
	// Start is the first block to process (the Omni genesis block).
	Start int64
	// Poll is the wait between checks for new blocks once synced.
	Poll time.Duration
	Log  *slog.Logger

	nodeHeight int64
	lastLog    time.Time
}

// Run syncs until ctx is cancelled.
func (s *Syncer) Run(ctx context.Context) error {
	for {
		progressed, err := s.Step(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var nodeErr *NodeError
		if errors.As(err, &nodeErr) && !errors.Is(err, rpc.ErrUnauthorized) {
			// The node may be restarting or briefly unreachable: keep trying.
			if s.Log != nil {
				s.Log.Warn("node request failed, retrying", "err", nodeErr.Err)
			}
			progressed = false
		} else if err != nil {
			return err
		}
		if progressed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.Poll):
		}
	}
}

// NodeError wraps a failed request to the node. Run retries these.
type NodeError struct{ Err error }

func (e *NodeError) Error() string { return "node: " + e.Err.Error() }
func (e *NodeError) Unwrap() error { return e.Err }

// Step connects or disconnects at most one block. It reports false when the
// store already matches the node's tip.
func (s *Syncer) Step(ctx context.Context) (bool, error) {
	tip, err := s.Store.Tip(ctx)
	if err != nil {
		return false, err
	}
	next := s.Start
	if tip != nil {
		next = tip.Height + 1
	}

	if next > s.nodeHeight {
		if s.nodeHeight, err = s.Src.GetBlockCount(ctx); err != nil {
			return false, &NodeError{err}
		}
	}
	if next > s.nodeHeight {
		// Caught up. Make sure our tip is still the node's block at that
		// height; a reorg to an equally long or shorter chain shows up only here.
		if tip == nil {
			return false, nil
		}
		if tip.Height <= s.nodeHeight {
			hash, err := s.Src.GetBlockHash(ctx, tip.Height)
			if err != nil {
				return false, &NodeError{err}
			}
			if hash == tip.Hash {
				return false, nil
			}
		}
		return true, s.disconnect(ctx)
	}

	hash, err := s.Src.GetBlockHash(ctx, next)
	if rpc.IsCode(err, rpc.CodeInvalidParameter) {
		// The node switched to a shorter chain since we last asked for its height.
		s.nodeHeight = 0
		return true, nil
	}
	if err != nil {
		return false, &NodeError{err}
	}
	hdr, err := s.Src.GetBlockHeader(ctx, hash)
	if err != nil {
		return false, &NodeError{err}
	}
	if hdr.Height != next {
		// The node reorganized between the two calls; ask again.
		return false, &NodeError{fmt.Errorf("block %s is at height %d, expected %d", hash, hdr.Height, next)}
	}
	if tip != nil && hdr.PreviousBlockHash != tip.Hash {
		return true, s.disconnect(ctx)
	}
	return true, s.connect(ctx, hdr)
}

func (s *Syncer) connect(ctx context.Context, hdr *rpc.BlockHeader) error {
	tx, err := s.Store.Begin(ctx, store.Block{
		Height:   hdr.Height,
		Hash:     hdr.Hash,
		PrevHash: hdr.PreviousBlockHash,
		Time:     hdr.Time,
	})
	if err != nil {
		return err
	}
	if err := s.Proc.ConnectBlock(ctx, tx, hdr); err != nil {
		tx.Rollback()
		return fmt.Errorf("block %d %s: %w", hdr.Height, hdr.Hash, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.Log != nil && (hdr.Height == s.nodeHeight || time.Since(s.lastLog) > 10*time.Second) {
		s.lastLog = time.Now()
		s.Log.Info("block connected", "height", hdr.Height, "node", s.nodeHeight, "hash", hdr.Hash)
	}
	return nil
}

func (s *Syncer) disconnect(ctx context.Context) error {
	b, err := s.Store.Disconnect(ctx)
	if errors.Is(err, store.ErrUndoUnavailable) {
		return fmt.Errorf("reorg below the kept undo data, rebuild the database: %w", err)
	}
	if err != nil {
		return err
	}
	if s.Log != nil {
		s.Log.Warn("block disconnected (reorg)", "height", b.Height, "hash", b.Hash)
	}
	return nil
}
