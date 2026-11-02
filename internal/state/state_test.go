package state

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

const (
	alice = "Lalice"
	bob   = "Lbob"
	carol = "Lcarol"
)

type harness struct {
	t      *testing.T
	st     *store.Store
	params *chain.Params
	opt    Options
	height int64
	n      int
	// fee is the miner fee of every transaction.
	fee int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), "main", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &harness{t: t, st: st, params: &chain.Main, height: chain.Main.OmniGenesis}
}

type txIn struct {
	sender string
	msg    omni.Message
}

// time is the block time: ten minutes per block from a fixed start.
func (h *harness) time() int64 { return 1_700_000_000 + 600*(h.height-h.params.OmniGenesis) }

// block applies the transactions in one block and returns their codes.
func (h *harness) block(txs ...txIn) []*Result {
	h.t.Helper()
	ctx := context.Background()
	prev := ""
	if tip, _ := h.st.Tip(ctx); tip != nil {
		prev = tip.Hash
	}
	hash := fmt.Sprintf("block%d", h.height)
	btx, err := h.st.Begin(ctx, store.Block{Height: h.height, Hash: hash, PrevHash: prev})
	if err != nil {
		h.t.Fatal(err)
	}
	s := New(btx, h.params, h.opt)
	if err := s.BeginBlock(h.height, h.time(), hash); err != nil {
		btx.Rollback()
		h.t.Fatal(err)
	}
	var res []*Result
	for i, tx := range txs {
		h.n++
		m := tx.msg
		r, err := s.Apply(&Tx{Txid: fmt.Sprintf("tx%d", h.n), Block: h.height, Index: i + 1, BlockHash: hash, BlockTime: h.time(), Sender: tx.sender, Fee: h.fee, Msg: &m})
		if err != nil {
			btx.Rollback()
			h.t.Fatal(err)
		}
		res = append(res, r)
	}
	if err := s.EndBlock(h.height, h.time()); err != nil {
		btx.Rollback()
		h.t.Fatal(err)
	}
	if err := btx.Commit(); err != nil {
		h.t.Fatal(err)
	}
	h.height++
	return res
}

// read runs fn on the current state in a throwaway block transaction.
func (h *harness) read(fn func(s *State)) {
	h.t.Helper()
	ctx := context.Background()
	prev := ""
	if tip, _ := h.st.Tip(ctx); tip != nil {
		prev = tip.Hash
	}
	btx, err := h.st.Begin(ctx, store.Block{Height: h.height, Hash: "probe", PrevHash: prev})
	if err != nil {
		h.t.Fatal(err)
	}
	defer btx.Rollback()
	fn(New(btx, h.params, h.opt))
}

func (h *harness) balance(addr string, id uint32) int64 {
	h.t.Helper()
	var b int64
	h.read(func(s *State) {
		var err error
		if b, err = s.Balance(addr, id, Balance); err != nil {
			h.t.Fatal(err)
		}
	})
	return b
}

func codes(res []*Result) []int {
	var c []int
	for _, r := range res {
		c = append(c, r.Code)
	}
	return c
}

func wantCodes(t *testing.T, res []*Result, want ...int) {
	t.Helper()
	got := codes(res)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
}

func fixed(eco uint8, amount uint64, name string) omni.Message {
	return omni.Message{Type: omni.TypeCreatePropertyFixed, Ecosystem: eco, PropType: PropTypeDivisible, Amount: amount, Name: name}
}

func managed(name string) omni.Message {
	return omni.Message{Type: omni.TypeCreatePropertyMan, Ecosystem: 1, PropType: PropTypeIndivisible, Name: name}
}

func send(id uint32, amount uint64, to string) omni.Message {
	return omni.Message{Type: omni.TypeSimpleSend, Property: id, Amount: amount, Receiver: to}
}

func grant(id uint32, amount uint64, to string) omni.Message {
	return omni.Message{Type: omni.TypeGrantTokens, Property: id, Amount: amount, Receiver: to}
}

func TestFixedIssuanceAndSend(t *testing.T) {
	h := newHarness(t)
	bad := fixed(1, 5, "Bad")
	bad.PropType = 7
	wantCodes(t, h.block(
		txIn{alice, fixed(3, 5, "Eco")},
		txIn{alice, fixed(1, 0, "Zero")},
		txIn{alice, bad},
		txIn{alice, fixed(1, 5, "")},
		txIn{alice, fixed(1, 1000, "Main")},
		txIn{alice, fixed(2, 70, "Test")},
	), -40021, -40023, -40036, -40037, 0, 0)

	// Invalid issuances do not use up IDs.
	if b := h.balance(alice, 3); b != 1000 {
		t.Fatalf("alice property 3 = %d", b)
	}
	if b := h.balance(alice, TestEcoFirst); b != 70 {
		t.Fatalf("alice test property = %d", b)
	}

	wantCodes(t, h.block(
		txIn{alice, send(3, 300, bob)},
		txIn{alice, send(3, 701, bob)},
		txIn{alice, send(99, 1, bob)},
		txIn{alice, send(0, 1, bob)},
		txIn{alice, send(3, 0, bob)},
		txIn{alice, send(3, math.MaxInt64+1, bob)},
	), 0, -60025, -60024, -60022, -60023, -60023)
	if a, b := h.balance(alice, 3), h.balance(bob, 3); a != 700 || b != 300 {
		t.Fatalf("balances alice %d bob %d", a, b)
	}
	h.read(func(s *State) {
		if total, _ := s.TotalTokens(3); total != 1000 {
			t.Fatalf("total = %d", total)
		}
	})
}

func TestManagedProperty(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, managed("Managed")}, txIn{alice, fixed(1, 10, "Fixed")})
	const m, f = 3, 4

	wantCodes(t, h.block(
		txIn{alice, grant(m, 500, bob)},
		txIn{bob, grant(m, 1, bob)},
		txIn{alice, grant(f, 1, alice)},
		txIn{alice, grant(m, math.MaxInt64-499, alice)},
		txIn{alice, grant(m, math.MaxInt64-500, alice)},
	), 0, -82043, -82042, -82044, 0)

	revoke := func(id uint32, amount uint64) omni.Message {
		return omni.Message{Type: omni.TypeRevokeTokens, Property: id, Amount: amount}
	}
	wantCodes(t, h.block(
		txIn{bob, revoke(m, 200)},
		txIn{bob, revoke(m, 301)},
		txIn{alice, revoke(f, 1)},
	), 0, -82025, -82042)
	if b := h.balance(bob, m); b != 300 {
		t.Fatalf("bob = %d", b)
	}
	h.read(func(s *State) {
		total, _ := s.TotalTokens(m)
		p, _ := s.Property(m)
		if total != math.MaxInt64-200 || len(p.History) != 3 {
			t.Fatalf("total %d history %+v", total, p.History)
		}
	})
}

func TestChangeIssuer(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, managed("Managed")})
	change := omni.Message{Type: omni.TypeChangeIssuer, Property: 3, Receiver: carol}

	wantCodes(t, h.block(
		txIn{bob, change},
		txIn{alice, change},
		// Later in the same block the new issuer is already in charge.
		txIn{carol, grant(3, 5, carol)},
		txIn{alice, grant(3, 5, alice)},
	), -82043, 0, 0, -82043)

	noRef := change
	noRef.Receiver = ""
	wantCodes(t, h.block(txIn{carol, noRef}), -82045)
}

func TestIssuerChangeLaterInBlockCounts(t *testing.T) {
	// getIssuer only compares block heights: a change at a later position
	// of the same block already counts for an earlier transaction.
	p := &Property{Issuer: bob}
	p.updateIssuer(10, 1, alice)
	p.updateIssuer(12, 5, bob)
	p.updateIssuer(12, 2, carol)
	if p.IssuerAt(11) != alice || p.IssuerAt(12) != bob || p.IssuerAt(9) != bob {
		t.Fatalf("issuers %+v", p.Issuers)
	}
}

func TestSendAll(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, fixed(1, 10, "A")}, txIn{alice, fixed(1, 20, "B")}, txIn{alice, fixed(2, 30, "T")})
	sendAll := omni.Message{Type: omni.TypeSendAll, Ecosystem: 1, Property: 1, Receiver: bob}

	res := h.block(txIn{alice, sendAll}, txIn{alice, sendAll}, txIn{carol, sendAll})
	wantCodes(t, res, 0, -83055, -83054)
	if res[0].NewAmount != 2 || len(res[0].SendAll) != 2 || res[0].SendAll[0] != (SendAllItem{3, 10}) || res[0].SendAll[1] != (SendAllItem{4, 20}) {
		t.Fatalf("send all = %+v", res[0])
	}
	if h.balance(bob, 4) != 20 || h.balance(alice, TestEcoFirst) != 30 {
		t.Fatal("wrong balances after send all")
	}
}

func TestActivation(t *testing.T) {
	h := newHarness(t)
	act := func(feature uint16, at int64, minVersion uint32) omni.Message {
		return omni.Message{Type: omni.TypeActivation, Version: 0xFFFF, FeatureID: feature, ActivationBlock: uint32(at), MinClientVersion: minVersion}
	}
	at := h.height + 8192
	v0 := act(chain.FeatureSTOv1, at, 0)
	v0.Version = 0
	wantCodes(t, h.block(
		txIn{alice, act(chain.FeatureSTOv1, at, 0)},
		txIn{chain.Authority, v0},
		txIn{chain.Authority, act(chain.FeatureSTOv1, h.height+100, 0)},
		txIn{chain.Authority, act(chain.FeatureSTOv1, at, 0)},
	), -9051, -9022, -9054, 0)

	h.read(func(s *State) {
		if active, _ := s.FeatureActive(chain.FeatureSTOv1, at-1); active {
			t.Fatal("active too early")
		}
		if active, _ := s.FeatureActive(chain.FeatureSTOv1, at); !active {
			t.Fatal("not active at the activation block")
		}
		// STO v1 is allowed from the activation block on.
		if ok, _ := s.typeAllowed(at, 3, omni.TypeSendToOwners, 1); !ok {
			t.Fatal("STO v1 not allowed after activation")
		}
	})

	// A feature that needs a newer client stops the indexer when it goes live.
	h.opt.ActivationAllowSenders = []string{"any"}
	h.block(txIn{bob, act(99, h.height+8192, OmniCoreVersion+1)})
	ctx := context.Background()
	tip, _ := h.st.Tip(ctx)
	btx, err := h.st.Begin(ctx, store.Block{Height: tip.Height + 1, Hash: "x", PrevHash: tip.Hash})
	if err != nil {
		t.Fatal(err)
	}
	defer btx.Rollback()
	var unsupported *ErrUnsupportedActivation
	if err := New(btx, h.params, h.opt).BeginBlock(tip.Height+8192, 0, "x"); !errors.As(err, &unsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestAlerts(t *testing.T) {
	h := newHarness(t)
	h.opt.AlertAllowSenders = []string{alice}
	alert := func(typ uint16, expiry uint32) omni.Message {
		return omni.Message{Type: omni.TypeAlert, Version: 0xFFFF, AlertType: typ, AlertExpiry: expiry, AlertText: "hello"}
	}
	expiry := uint32(h.height + 2)
	wantCodes(t, h.block(txIn{bob, alert(1, expiry)}, txIn{alice, alert(1, expiry)}, txIn{alice, alert(9, expiry)}), -9051, 0, 0)

	count := func() int {
		var n int
		h.read(func(s *State) {
			kvs, _ := s.tx.Scan(NsAlert, "")
			n = len(kvs)
		})
		return n
	}
	if count() != 1 {
		t.Fatalf("alerts = %d, want 1 (type 9 is rejected)", count())
	}
	h.block()
	h.block() // reaches the expiry block
	if count() != 0 {
		t.Fatal("alert did not expire")
	}

	h.block(txIn{alice, alert(2, math.MaxUint32)})
	h.block(txIn{alice, alert(0xFFFF, 0)})
	if count() != 0 {
		t.Fatal("alert not deleted")
	}
}

func TestReorgRestoresState(t *testing.T) {
	h := newHarness(t)
	h.block(txIn{alice, fixed(1, 100, "A")})
	h.block(txIn{alice, send(3, 40, bob)}, txIn{bob, fixed(1, 5, "B")})
	if _, err := h.st.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.height--
	if h.balance(alice, 3) != 100 || h.balance(bob, 3) != 0 {
		t.Fatal("balances not restored")
	}
	h.read(func(s *State) {
		if p, _ := s.Property(4); p != nil {
			t.Fatal("property 4 survived the reorg")
		}
		if id, _ := s.nextID(1); id != 4 {
			t.Fatalf("next id = %d", id)
		}
	})
}
