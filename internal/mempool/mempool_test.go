package mempool

import (
	"context"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
)

type fakeNode struct {
	mempool []string
	fetches int
	// during runs inside GetRawMempool, after the list is taken.
	during func()
}

func (n *fakeNode) GetRawMempool(context.Context) ([]string, error) {
	list := n.mempool
	if n.during != nil {
		n.during()
	}
	return list, nil
}

func (n *fakeNode) GetRawTransaction(_ context.Context, txid string) (*rpc.Tx, error) {
	n.fetches++
	return &rpc.Tx{Txid: txid}, nil
}

// fakeDecoder treats txids starting with "omni" as Omni transactions.
type fakeDecoder struct{}

func (fakeDecoder) DecodeTx(_ context.Context, t *rpc.Tx) (*indexer.Record, *omni.Message, error) {
	if len(t.Txid) < 4 || t.Txid[:4] != "omni" {
		return nil, nil, nil
	}
	return &indexer.Record{Txid: t.Txid, Sender: "Lsender"}, &omni.Message{}, nil
}

func TestRefresh(t *testing.T) {
	ctx := context.Background()
	node := &fakeNode{mempool: []string{"omni1", "plain", "omni2"}}
	p := &Pool{Node: node, Dec: fakeDecoder{}}
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if list := p.List(); len(list) != 2 || p.Get("plain") != nil || p.Get("omni1") == nil {
		t.Fatalf("list = %v", list)
	}

	// Known transactions are not fetched again.
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if node.fetches != 3 {
		t.Fatalf("fetches = %d", node.fetches)
	}

	node.mempool = []string{"omni2"}
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Get("omni1") != nil || len(p.List()) != 1 {
		t.Fatal("transaction that left the mempool is still listed")
	}
}

func TestPending(t *testing.T) {
	ctx := context.Background()
	node := &fakeNode{mempool: []string{"omniA", "omniB"}}
	p := &Pool{Node: node, Dec: fakeDecoder{}}
	p.AddPending("omniA", "Lme", 3, 40)
	p.AddPending("omniB", "Lme", 3, 2)
	p.AddPending("other", "Lme", 4, 7)
	if d := p.PendingDebit("Lme", 3); d != 42 {
		t.Fatalf("debit = %d", d)
	}

	// Confirmed in a block: the debit goes, the balance itself has dropped.
	p.Confirmed("omniA")
	if d := p.PendingDebit("Lme", 3); d != 2 {
		t.Fatalf("debit after confirm = %d", d)
	}

	// Not in the mempool any more (dropped or never relayed).
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if d := p.PendingDebit("Lme", 4); d != 0 {
		t.Fatalf("debit of a dropped tx = %d", d)
	}

	// After a reorg a confirmed transaction returns to the mempool and is
	// decoded again.
	p.Confirmed("omniB")
	if p.Get("omniB") != nil {
		t.Fatal("confirmed transaction still listed")
	}
	before := node.fetches
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Get("omniB") == nil || node.fetches != before+1 {
		t.Fatalf("omniB after reorg: %v, fetches %d -> %d", p.Get("omniB"), before, node.fetches)
	}
}

func TestSent(t *testing.T) {
	ctx := context.Background()
	node := &fakeNode{}
	p := &Pool{Node: node, Dec: fakeDecoder{}}
	sent := func(id string) {
		p.Sent(&Entry{Record: &indexer.Record{Txid: id, Sender: "Lme"}, Msg: &omni.Message{}}, 3, 10)
	}
	sent("omniA")
	// Sent while the mempool list was being fetched: kept.
	node.during = func() { sent("omniB") }
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	node.during = nil
	if p.Get("omniA") != nil || p.Get("omniB") == nil || p.PendingDebit("Lme", 3) != 10 {
		t.Fatalf("after refresh: A %v, B %v", p.Get("omniA"), p.Get("omniB"))
	}
	if own := p.Own(); len(own) != 1 || own[0].Record.Txid != "omniB" {
		t.Fatalf("own = %v", own)
	}
	// Still missing on the next round: dropped.
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Get("omniB") != nil || len(p.Own()) != 0 {
		t.Fatal("dropped transaction still pending")
	}
}
