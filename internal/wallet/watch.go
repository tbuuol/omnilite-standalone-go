package wallet

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// The payment watcher follows the DEx accepts of the node wallet's
// addresses and reports how many blocks are left to pay. It never pays:
// completing a purchase is always omni_senddexpay, called by the user.

// mineCache remembers getaddressinfo answers for a while.
type mineCache struct {
	mu   sync.Mutex
	seen map[string]mineEntry
}

type mineEntry struct {
	mine bool
	at   time.Time
}

const mineCacheTTL = 5 * time.Minute

// IsMine reports whether the node wallet holds the key of addr.
func (w *Wallet) IsMine(ctx context.Context, addr string) (bool, error) {
	w.mine.mu.Lock()
	e, ok := w.mine.seen[addr]
	w.mine.mu.Unlock()
	if ok && time.Since(e.at) < mineCacheTTL {
		return e.mine, nil
	}
	var info addressInfo
	if err := w.Node.Call(ctx, "getaddressinfo", &info, addr); err != nil {
		return false, err
	}
	w.mine.mu.Lock()
	if w.mine.seen == nil {
		w.mine.seen = map[string]mineEntry{}
	}
	w.mine.seen[addr] = mineEntry{info.IsMine, time.Now()}
	w.mine.mu.Unlock()
	return info.IsMine, nil
}

// OpenAccept is a DEx accept with a wallet address as buyer or seller.
type OpenAccept struct {
	Seller, Buyer string
	Property      uint32
	// Amount is the token amount still to be paid for, AmountToPay the LTC.
	Amount      int64
	AmountToPay int64
	Block       int64
	// BlocksLeft counts the blocks that can still include the payment.
	BlocksLeft int64
	MineBuyer  bool
	MineSeller bool
}

// OpenAccepts lists the accepts of the wallet's addresses at the index tip.
func (w *Wallet) OpenAccepts(ctx context.Context) ([]OpenAccept, error) {
	var all []OpenAccept
	err := w.Store.Read(ctx, func(snap *store.Snapshot) error {
		tip, err := snap.Tip()
		if err != nil || tip == nil {
			return err
		}
		accepts, err := state.New(snap, w.Params, w.State).Accepts()
		if err != nil {
			return err
		}
		for _, a := range accepts {
			all = append(all, OpenAccept{
				Seller: a.Seller, Buyer: a.Buyer, Property: a.Property, Amount: a.AmountRemaining,
				AmountToPay: state.DesiredLTC(a.OfferAmountOriginal, a.LTCDesiredOriginal, a.AmountRemaining),
				Block:       a.Block, BlocksLeft: a.Block + int64(a.BlockTimeLimit) - tip.Height,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var mine []OpenAccept
	for _, a := range all {
		if a.MineBuyer, err = w.IsMine(ctx, a.Buyer); err != nil {
			return nil, err
		}
		if a.MineSeller, err = w.IsMine(ctx, a.Seller); err != nil {
			return nil, err
		}
		if a.MineBuyer || a.MineSeller {
			mine = append(mine, a)
		}
	}
	return mine, nil
}

// WarnBlocks is how many blocks before the end of a payment window the
// watcher starts warning.
const WarnBlocks = 3

// Watch logs the wallet's open accepts until ctx ends: once when an accept
// appears, on every block while few blocks are left, and when it is gone.
func (w *Wallet) Watch(ctx context.Context, poll time.Duration) {
	known := map[string]int64{} // accept -> blocks left last reported
	for {
		list, err := w.OpenAccepts(ctx)
		if err != nil {
			if ctx.Err() == nil && w.Log != nil {
				w.Log.Warn("dex watcher", "err", err)
			}
		} else {
			current := map[string]bool{}
			for _, a := range list {
				key := fmt.Sprintf("%s-%d+%s", a.Seller, a.Property, a.Buyer)
				current[key] = true
				last, seen := known[key]
				known[key] = a.BlocksLeft
				if w.Log == nil || !a.MineBuyer {
					continue
				}
				switch {
				case !seen:
					w.Log.Info("dex accept open, pay with omni_senddexpay", "buyer", a.Buyer, "seller", a.Seller,
						"property", a.Property, "ltc", formatLTC(a.AmountToPay), "blocksleft", a.BlocksLeft)
				case a.BlocksLeft <= WarnBlocks && a.BlocksLeft != last:
					w.Log.Warn("dex accept about to expire, unpaid LTC buys nothing afterwards", "buyer", a.Buyer, "seller", a.Seller,
						"property", a.Property, "ltc", formatLTC(a.AmountToPay), "blocksleft", a.BlocksLeft)
				}
			}
			for key := range known {
				if !current[key] {
					delete(known, key)
					if w.Log != nil {
						w.Log.Info("dex accept closed (paid or expired)", "accept", key)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
