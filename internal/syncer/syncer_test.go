package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// fakeChain is a node whose best chain can be replaced to simulate reorgs.
// hashes[i] is the block at height i.
type fakeChain struct {
	hashes []string
}

func newChain(tag string, n int) *fakeChain {
	c := &fakeChain{}
	c.extend(tag, n)
	return c
}

func (c *fakeChain) extend(tag string, n int) {
	for len(c.hashes) < n {
		c.hashes = append(c.hashes, fmt.Sprintf("%s-%d", tag, len(c.hashes)))
	}
}

// fork replaces everything from height on with a new branch of total length n.
func (c *fakeChain) fork(tag string, height, n int) {
	c.hashes = c.hashes[:height]
	c.extend(tag, n)
}

func (c *fakeChain) GetBlockCount(context.Context) (int64, error) {
	return int64(len(c.hashes) - 1), nil
}

func (c *fakeChain) GetBlockHash(_ context.Context, h int64) (string, error) {
	if h < 0 || h >= int64(len(c.hashes)) {
		return "", &rpc.Error{Code: rpc.CodeInvalidParameter, Message: "Block height out of range"}
	}
	return c.hashes[h], nil
}

func (c *fakeChain) GetBlockHeader(_ context.Context, hash string) (*rpc.BlockHeader, error) {
	for h, x := range c.hashes {
		if x == hash {
			prev := ""
			if h > 0 {
				prev = c.hashes[h-1]
			}
			return &rpc.BlockHeader{Hash: hash, Height: int64(h), PreviousBlockHash: prev}, nil
		}
	}
	return nil, &rpc.Error{Code: rpc.CodeInvalidAddressKey, Message: "Block not found"}
}

// countingProcessor stores the hash per height and a running block counter,
// so the test sees whether the state follows the active chain exactly.
type countingProcessor struct{}

func (countingProcessor) ConnectBlock(_ context.Context, tx *store.BlockTx, hdr *rpc.BlockHeader) error {
	if err := tx.Put("blk", strconv.FormatInt(hdr.Height, 10), []byte(hdr.Hash)); err != nil {
		return err
	}
	v, _, err := tx.Get("cnt", "n")
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(string(v))
	return tx.Put("cnt", "n", []byte(strconv.Itoa(n+1)))
}

const start = 3

func setup(t *testing.T, chain *fakeChain, undoDepth int64) *Syncer {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), "regtest", undoDepth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Syncer{Src: chain, Store: st, Proc: countingProcessor{}, Start: start}
}

func syncAll(t *testing.T, s *Syncer) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		progressed, err := s.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			return
		}
	}
	t.Fatal("sync did not settle")
}

// assertMatches checks that store blocks and state equal the chain from start on.
func assertMatches(t *testing.T, s *Syncer, chain *fakeChain) {
	t.Helper()
	ctx := context.Background()
	tip, err := s.Store.Tip(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantTip := int64(len(chain.hashes) - 1)
	if tip == nil || tip.Height != wantTip || tip.Hash != chain.hashes[wantTip] {
		t.Fatalf("tip = %+v, want height %d hash %s", tip, wantTip, chain.hashes[wantTip])
	}
	for h := int64(start); h <= wantTip; h++ {
		b, err := s.Store.BlockAt(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := s.Store.Get(ctx, "blk", strconv.FormatInt(h, 10))
		if err != nil {
			t.Fatal(err)
		}
		if b == nil || b.Hash != chain.hashes[h] || string(v) != chain.hashes[h] {
			t.Fatalf("height %d: block %+v, state %q, want %s", h, b, v, chain.hashes[h])
		}
	}
	if b, _ := s.Store.BlockAt(ctx, wantTip+1); b != nil {
		t.Fatalf("stale block above tip: %+v", b)
	}
	v, _, _ := s.Store.Get(ctx, "cnt", "n")
	if want := strconv.FormatInt(wantTip-start+1, 10); string(v) != want {
		t.Fatalf("block counter = %s, want %s", v, want)
	}
}

func TestSyncFromStart(t *testing.T) {
	chain := newChain("a", 11)
	s := setup(t, chain, 100)
	syncAll(t, s)
	assertMatches(t, s, chain)

	chain.extend("a", 15)
	syncAll(t, s)
	assertMatches(t, s, chain)
}

func TestReorgToLongerChain(t *testing.T) {
	chain := newChain("a", 11)
	s := setup(t, chain, 100)
	syncAll(t, s)

	chain.fork("b", 7, 13)
	syncAll(t, s)
	assertMatches(t, s, chain)
}

func TestReorgToShorterChain(t *testing.T) {
	chain := newChain("a", 13)
	s := setup(t, chain, 100)
	syncAll(t, s)

	chain.fork("c", 5, 9)
	syncAll(t, s)
	assertMatches(t, s, chain)
}

func TestReorgSameHeight(t *testing.T) {
	chain := newChain("a", 11)
	s := setup(t, chain, 100)
	syncAll(t, s)

	chain.fork("d", 10, 11)
	syncAll(t, s)
	assertMatches(t, s, chain)
}

func TestReorgBelowStart(t *testing.T) {
	chain := newChain("a", 11)
	s := setup(t, chain, 100)
	syncAll(t, s)

	chain.fork("e", 1, 11)
	syncAll(t, s)
	assertMatches(t, s, chain)
}

func TestReorgDeeperThanUndo(t *testing.T) {
	chain := newChain("a", 40)
	s := setup(t, chain, 10)
	syncAll(t, s)

	chain.fork("f", 20, 41)
	for i := 0; i < 100; i++ {
		if _, err := s.Step(context.Background()); err != nil {
			return
		}
	}
	t.Fatal("reorg deeper than undodepth did not fail")
}
