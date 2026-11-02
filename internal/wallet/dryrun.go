package wallet

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/mempool"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// RefusedError reports a DEx payment the dry-run rejected: it would buy
// no tokens, so the LTC would be lost.
type RefusedError struct {
	Message string
}

func (e *RefusedError) Error() string { return e.Message }

// dryRun decodes the built transaction like the indexer will and applies
// it to a copy of the current state, after the own transactions still
// waiting in the mempool. It fails unless the transaction would be valid.
func (w *Wallet) dryRun(ctx context.Context, req *Request, b *built) (*indexer.Record, *omni.Message, error) {
	rec, msg, err := w.Dec.DecodeTx(ctx, rpcTx(b.tx))
	if err != nil {
		return nil, nil, err
	}
	if req.Pay != nil {
		if rec == nil || rec.Kind != indexer.KindDExPayment {
			return nil, nil, fmt.Errorf("the built transaction is not read as a DEx payment")
		}
	} else if rec == nil || msg == nil {
		return nil, nil, fmt.Errorf("the built transaction is not a valid Omni transaction")
	}
	// The decoder must see what was meant to be sent.
	if rec.Sender != req.From {
		return nil, nil, fmt.Errorf("the built transaction would be sent from %s, not %s", rec.Sender, req.From)
	}
	if req.To != "" && rec.Reference != req.To {
		return nil, nil, fmt.Errorf("the built transaction would reference %s, not %s", rec.Reference, req.To)
	}
	if req.Pay == nil && rec.Payload != hex.EncodeToString(req.Payload) {
		// Class B pads the last packet with zeros.
		if p := hex.EncodeToString(req.Payload); len(rec.Payload) < len(p) || rec.Payload[:len(p)] != p {
			return nil, nil, fmt.Errorf("the built transaction carries a different payload")
		}
	}

	var height int64
	if err := w.Node.Call(ctx, "getblockcount", &height); err != nil {
		return nil, nil, err
	}
	var res *state.Result
	err = w.Store.Read(ctx, func(snap *store.Snapshot) error {
		tip, err := snap.Tip()
		if err != nil {
			return err
		}
		if tip == nil || tip.Height != height {
			at := int64(-1)
			if tip != nil {
				at = tip.Height
			}
			return fmt.Errorf("the index is not in sync with the node (index %d, node %d), try again shortly", at, height)
		}
		st := state.New(store.NewOverlay(snap), w.Params, w.State)
		block := tip.Height + 1
		now := max(time.Now().Unix(), tip.Time)
		if err := st.BeginBlock(block, now, ""); err != nil {
			return err
		}
		index := 0
		for _, e := range w.Pool.Own() {
			if _, err := apply(st, e, block, index, now); err != nil {
				return err
			}
			index++
		}
		res, err = apply(st, &mempool.Entry{Record: rec, Msg: msg}, block, index, now)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if req.Pay != nil {
		if res != nil {
			for _, p := range res.Payments {
				if p.Seller == req.Pay.Seller && p.Property == req.Pay.Property && p.Amount > 0 {
					return rec, nil, nil
				}
			}
		}
		return nil, nil, &RefusedError{Message: "The payment would buy no tokens (accept expired, offer changed or nothing left), not sent"}
	}
	if !res.Valid {
		return nil, nil, &InvalidError{Code: res.Code}
	}
	return rec, msg, nil
}

// apply runs an Omni transaction or a DEx payment on st as part of block.
func apply(st *state.State, e *mempool.Entry, block int64, index int, now int64) (*state.Result, error) {
	r := e.Record
	if e.Msg == nil {
		return st.ApplyPayment(&state.Payment{Txid: r.Txid, Block: block, Sender: r.Sender, Outputs: r.Payments})
	}
	return st.Apply(&state.Tx{Txid: r.Txid, Block: block, Index: index, BlockTime: now,
		Sender: r.Sender, Fee: r.Fee, Msg: e.Msg})
}
