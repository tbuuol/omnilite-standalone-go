// Package mempool follows the node's mempool and keeps the unconfirmed Omni
// transactions decoded, for omni_listpendingtransactions and
// omni_gettransaction. It also holds the pending debits of transactions this
// program sent itself (pending.cpp), which lower the available balance until
// they confirm or leave the mempool.
package mempool

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
)

// Node is what the pool needs from litecoind.
type Node interface {
	GetRawMempool(ctx context.Context) ([]string, error)
	GetRawTransaction(ctx context.Context, txid string) (*rpc.Tx, error)
}

// Decoder turns a raw transaction into an Omni record.
type Decoder interface {
	DecodeTx(ctx context.Context, t *rpc.Tx) (*indexer.Record, *omni.Message, error)
}

// Entry is an unconfirmed Omni transaction.
type Entry struct {
	Record *indexer.Record
	Msg    *omni.Message
	Seen   time.Time
}

// pending is a debit registered with AddPending (CMPPending).
type pending struct {
	sender   string
	property uint32
	amount   int64
}

type Pool struct {
	Node Node
	Dec  Decoder
	Poll time.Duration
	Log  *slog.Logger

	mu      sync.RWMutex
	entries map[string]*Entry
	// seen holds every txid already looked at, Omni or not, so each
	// mempool transaction is fetched once.
	seen    map[string]bool
	pending map[string]pending
}

func (p *Pool) init() {
	if p.entries == nil {
		p.entries = map[string]*Entry{}
		p.seen = map[string]bool{}
		p.pending = map[string]pending{}
	}
}

// Run polls the mempool until ctx ends.
func (p *Pool) Run(ctx context.Context) {
	for {
		if err := p.Refresh(ctx); err != nil && ctx.Err() == nil && p.Log != nil {
			p.Log.Warn("mempool refresh failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Poll):
		}
	}
}

// Refresh syncs the pool with the node's mempool once.
func (p *Pool) Refresh(ctx context.Context) error {
	txids, err := p.Node.GetRawMempool(ctx)
	if err != nil {
		return err
	}
	current := make(map[string]bool, len(txids))
	var fresh []string
	p.mu.RLock()
	for _, id := range txids {
		current[id] = true
		if !p.seen[id] {
			fresh = append(fresh, id)
		}
	}
	p.mu.RUnlock()

	decoded := map[string]*Entry{}
	for _, id := range fresh {
		t, err := p.Node.GetRawTransaction(ctx, id)
		if rpc.IsCode(err, rpc.CodeInvalidAddressKey) {
			continue // left the mempool meanwhile
		}
		if err != nil {
			return err
		}
		rec, msg, err := p.Dec.DecodeTx(ctx, t)
		if err != nil {
			var nodeErr *rpc.Error
			if errors.As(err, &nodeErr) {
				// A parent left the mempool; try again next round.
				continue
			}
			return err
		}
		if rec == nil || msg == nil {
			// Not Omni, or a DEx payment: unconfirmed payments are not
			// shown by OmniLite either.
			decoded[id] = nil
			continue
		}
		decoded[id] = &Entry{Record: rec, Msg: msg, Seen: time.Now()}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	for id, e := range decoded {
		p.seen[id] = true
		if e != nil {
			p.entries[id] = e
		}
	}
	// PendingCheck: forget everything that left the mempool.
	for id := range p.seen {
		if !current[id] {
			delete(p.seen, id)
			delete(p.entries, id)
			delete(p.pending, id)
		}
	}
	for id := range p.pending {
		if !current[id] {
			delete(p.pending, id)
		}
	}
	return nil
}

// Get returns an unconfirmed Omni transaction, or nil.
func (p *Pool) Get(txid string) *Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.entries[txid]
}

// List returns the unconfirmed Omni transactions, oldest first.
func (p *Pool) List() []*Entry {
	p.mu.RLock()
	list := make([]*Entry, 0, len(p.entries))
	for _, e := range p.entries {
		list = append(list, e)
	}
	p.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Seen.Equal(list[j].Seen) {
			return list[i].Seen.Before(list[j].Seen)
		}
		return list[i].Record.Txid < list[j].Record.Txid
	})
	return list
}

// AddPending registers a debit for a transaction sent by this program
// (PendingAdd with fSubtract).
func (p *Pool) AddPending(txid, sender string, property uint32, amount int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.pending[txid] = pending{sender, property, amount}
}

// Confirmed drops the pending debit of a transaction that made it into a
// block (PendingDelete in mastercore_handler_tx).
func (p *Pool) Confirmed(txid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pending, txid)
	delete(p.entries, txid)
	// Forget it was seen: after a reorg it may come back into the mempool.
	delete(p.seen, txid)
}

// PendingDebit is the amount still pending to leave an address
// (the negative PENDING tally).
func (p *Pool) PendingDebit(addr string, property uint32) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var sum int64
	for _, d := range p.pending {
		if d.sender == addr && d.property == property {
			sum += d.amount
		}
	}
	return sum
}
