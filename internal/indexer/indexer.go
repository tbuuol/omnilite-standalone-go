// Package indexer turns blocks into Omni transaction records. It is the
// syncer's Processor: everything it writes goes through the block's store
// transaction, so reorgs undo it.
package indexer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
	"github.com/tbuuol/omnilite-standalone/internal/syncer"
)

// Store namespaces.
const (
	// NsTx maps txid to its Record (JSON).
	NsTx = "tx"
	// NsBlockTx maps BlockTxKey(height, index) to the txid, giving the
	// processing order.
	NsBlockTx = "blocktx"
)

// BlockTxKey orders transactions by height and position in the block.
func BlockTxKey(height int64, index int) string { return fmt.Sprintf("%010d:%05d", height, index) }

// Kinds of records.
const (
	KindOmni       = "omni"   // a payload that interpret_Transaction accepts
	KindDExPayment = "dexpay" // a Class A transaction without data, maybe a DEx payment
)

// Record is a decoded transaction. The payload is kept raw and interpreted
// again where needed, so property strings survive byte for byte.
type Record struct {
	Kind      string            `json:"kind"`
	Txid      string            `json:"txid"`
	Block     int64             `json:"block"`
	Index     int               `json:"index"`
	Time      int64             `json:"time"`
	Class     string            `json:"class"`
	Sender    string            `json:"sender"`
	Reference string            `json:"reference,omitempty"`
	Fee       int64             `json:"fee"`
	Version   uint16            `json:"version"`
	Type      uint16            `json:"type"`
	Payload   string            `json:"payload,omitempty"`
	Payments  []omni.DExPayment `json:"payments,omitempty"`
}

// Node is the part of the RPC client the indexer needs.
type Node interface {
	GetBlock(ctx context.Context, hash string) (*rpc.Block, error)
	GetRawTransaction(ctx context.Context, txid string) (*rpc.Tx, error)
}

type Indexer struct {
	Node Node
	Dec  *omni.Decoder
	Log  *slog.Logger
	// State holds the testing switches of the state engine.
	State state.Options
	// HashEvery logs the consensus hash every n blocks (0: off).
	HashEvery int64

	// prev caches outputs of transactions fetched for input lookups.
	prev map[string][]omni.Output
}

const prevCacheSize = 50_000

func (ix *Indexer) ConnectBlock(ctx context.Context, tx *store.BlockTx, hdr *rpc.BlockHeader) error {
	blk, err := ix.Node.GetBlock(ctx, hdr.Hash)
	if err != nil {
		return &syncer.NodeError{Err: err}
	}
	if blk.Hash != hdr.Hash || blk.Height != hdr.Height {
		return fmt.Errorf("node returned block %s at %d for %s at %d", blk.Hash, blk.Height, hdr.Hash, hdr.Height)
	}

	st := state.New(tx, ix.Dec.Params, ix.State)
	if err := st.BeginBlock(blk.Height, blk.Time, blk.Hash); err != nil {
		return err
	}

	inBlock := make(map[string]*rpc.Tx, len(blk.Tx))
	for i := range blk.Tx {
		inBlock[blk.Tx[i].Txid] = &blk.Tx[i]
	}

	for idx := range blk.Tx {
		t := &blk.Tx[idx]
		if t.IsCoinbase() {
			// OmniLite cannot load coinbase inputs and drops these (-101).
			continue
		}
		outs, err := convertOutputs(t)
		if err != nil {
			return fmt.Errorf("tx %s: %w", t.Txid, err)
		}
		if !ix.Dec.HasMarker(outs) || ix.Dec.EncodingClass(outs) == omni.NoMarker {
			continue
		}
		rec, msg, err := ix.decode(ctx, t, outs, inBlock)
		if err != nil {
			return err
		}
		if rec == nil {
			continue
		}
		rec.Block, rec.Index, rec.Time = blk.Height, idx, blk.Time
		v, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := tx.Put(NsTx, rec.Txid, v); err != nil {
			return err
		}
		if err := tx.Put(NsBlockTx, BlockTxKey(blk.Height, idx), []byte(rec.Txid)); err != nil {
			return err
		}
		if ix.Log != nil {
			ix.Log.Debug("omni transaction", "block", blk.Height, "txid", rec.Txid, "kind", rec.Kind, "type", rec.Type, "sender", rec.Sender)
		}
		if msg == nil {
			if _, err := st.ApplyPayment(&state.Payment{Txid: rec.Txid, Block: blk.Height, Sender: rec.Sender, Outputs: rec.Payments}); err != nil {
				return err
			}
			continue
		}
		if _, err := st.Apply(&state.Tx{
			Txid: rec.Txid, Block: blk.Height, Index: idx, BlockHash: blk.Hash, BlockTime: blk.Time,
			Sender: rec.Sender, Fee: rec.Fee, Msg: msg,
		}); err != nil {
			return err
		}
	}
	if err := st.EndBlock(blk.Height, blk.Time); err != nil {
		return err
	}
	if ix.HashEvery > 0 && blk.Height%ix.HashEvery == 0 && ix.Log != nil {
		h, err := st.ConsensusHash()
		if err != nil {
			return err
		}
		ix.Log.Info("consensus hash", "block", blk.Height, "hash", h)
	}
	return nil
}

// decode parses one marked transaction. It returns nil for transactions
// OmniLite drops without recording them. The message is nil for DEx payments.
func (ix *Indexer) decode(ctx context.Context, t *rpc.Tx, outs []omni.Output, inBlock map[string]*rpc.Tx) (*Record, *omni.Message, error) {
	ins := make([]omni.Output, len(t.Vin))
	for i, in := range t.Vin {
		o, err := ix.prevout(ctx, in.Txid, in.Vout, inBlock)
		if err != nil {
			return nil, nil, fmt.Errorf("tx %s input %d: %w", t.Txid, i, err)
		}
		ins[i] = o
	}

	p, err := ix.Dec.Parse(&omni.Tx{Txid: t.Txid, Outputs: outs, Inputs: ins})
	var rej *omni.RejectError
	if errors.As(err, &rej) {
		if ix.Log != nil {
			ix.Log.Debug("omni transaction rejected", "txid", t.Txid, "code", rej.Code, "reason", rej.Reason)
		}
		return nil, nil, nil
	}
	if err != nil || p == nil {
		return nil, nil, err
	}

	rec := &Record{Txid: t.Txid, Class: p.Class.String(), Sender: p.Sender, Reference: p.Reference, Fee: p.Fee}
	if p.IsDExPaymentHint() {
		rec.Kind = KindDExPayment
		rec.Payments = ix.Dec.DExPayments(outs)
		if len(rec.Payments) == 0 {
			return nil, nil, nil
		}
		return rec, nil, nil
	}
	m, err := omni.Interpret(p.Payload, p.Sender, p.Reference, ix.Dec.Params)
	if errors.Is(err, omni.ErrInvalidPayload) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rec.Kind, rec.Version, rec.Type = KindOmni, m.Version, m.Type
	rec.Payload = hex.EncodeToString(p.Payload)
	return rec, m, nil
}

func (ix *Indexer) prevout(ctx context.Context, txid string, vout uint32, inBlock map[string]*rpc.Tx) (omni.Output, error) {
	outs, ok := ix.prev[txid]
	if !ok {
		var t *rpc.Tx
		if bt, found := inBlock[txid]; found {
			t = bt
		} else {
			var err error
			if t, err = ix.Node.GetRawTransaction(ctx, txid); err != nil {
				return omni.Output{}, &syncer.NodeError{Err: fmt.Errorf("getrawtransaction %s: %w", txid, err)}
			}
		}
		var err error
		if outs, err = convertOutputs(t); err != nil {
			return omni.Output{}, err
		}
		if ix.prev == nil || len(ix.prev) >= prevCacheSize {
			ix.prev = make(map[string][]omni.Output)
		}
		ix.prev[txid] = outs
	}
	if int(vout) >= len(outs) {
		return omni.Output{}, fmt.Errorf("prevout %s:%d does not exist", txid, vout)
	}
	return outs[vout], nil
}

func convertOutputs(t *rpc.Tx) ([]omni.Output, error) {
	outs := make([]omni.Output, len(t.Vout))
	for i, o := range t.Vout {
		if int(o.N) != i {
			return nil, fmt.Errorf("output %d reported as n=%d", i, o.N)
		}
		script, err := hex.DecodeString(o.ScriptPubKey.Hex)
		if err != nil {
			return nil, fmt.Errorf("output %d script: %w", i, err)
		}
		v, err := rpc.Litoshi(o.Value)
		if err != nil {
			return nil, fmt.Errorf("output %d: %w", i, err)
		}
		outs[i] = omni.Output{Script: script, Value: v}
	}
	return outs, nil
}
