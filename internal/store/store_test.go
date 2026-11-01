package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T, undoDepth int64) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), "regtest", undoDepth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func hash(h int64) string { return fmt.Sprintf("h%d", h) }

func connect(t *testing.T, s *Store, h int64, fn func(tx *BlockTx)) {
	t.Helper()
	tx, err := s.Begin(context.Background(), Block{Height: h, Hash: hash(h), PrevHash: hash(h - 1)})
	if err != nil {
		t.Fatal(err)
	}
	if fn != nil {
		fn(tx)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantValue(t *testing.T, s *Store, key, want string, wantOK bool) {
	t.Helper()
	v, ok, err := s.Get(context.Background(), "test", key)
	must(t, err)
	if ok != wantOK || string(v) != want {
		t.Fatalf("%s = %q (exists %v), want %q (exists %v)", key, v, ok, want, wantOK)
	}
}

func TestDisconnectRestoresState(t *testing.T) {
	s := openTest(t, 100)
	ctx := context.Background()

	connect(t, s, 100, func(tx *BlockTx) {
		must(t, tx.Put("test", "a", []byte("1")))
		must(t, tx.Put("test", "empty", []byte{}))
	})
	connect(t, s, 101, func(tx *BlockTx) {
		must(t, tx.Put("test", "a", []byte("2")))
		must(t, tx.Put("test", "a", []byte("3")))
		must(t, tx.Put("test", "b", []byte("x")))
		must(t, tx.Delete("test", "empty"))
	})
	wantValue(t, s, "a", "3", true)
	wantValue(t, s, "b", "x", true)
	wantValue(t, s, "empty", "", false)

	b, err := s.Disconnect(ctx)
	must(t, err)
	if b.Height != 101 {
		t.Fatalf("disconnected %d, want 101", b.Height)
	}
	wantValue(t, s, "a", "1", true)
	wantValue(t, s, "b", "", false)
	wantValue(t, s, "empty", "", true)

	_, err = s.Disconnect(ctx)
	must(t, err)
	wantValue(t, s, "a", "", false)
	wantValue(t, s, "empty", "", false)
	tip, err := s.Tip(ctx)
	must(t, err)
	if tip != nil {
		t.Fatalf("tip = %+v, want empty store", tip)
	}
	if _, err := s.Disconnect(ctx); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestBeginRejectsBlockNotOnTip(t *testing.T) {
	s := openTest(t, 100)
	connect(t, s, 10, nil)
	for _, b := range []Block{
		{Height: 12, Hash: "x", PrevHash: hash(10)}, // gap
		{Height: 11, Hash: "x", PrevHash: "other"},  // wrong parent
	} {
		if _, err := s.Begin(context.Background(), b); !errors.Is(err, ErrNotOnTip) {
			t.Fatalf("%+v: err = %v, want ErrNotOnTip", b, err)
		}
	}
}

func TestUndoPruning(t *testing.T) {
	s := openTest(t, 10)
	ctx := context.Background()
	for h := int64(1); h <= 30; h++ {
		connect(t, s, h, func(tx *BlockTx) {
			must(t, tx.Put("test", "a", []byte(fmt.Sprint(h))))
		})
	}
	// Blocks 21..30 keep their undo data.
	for i := 0; i < 10; i++ {
		_, err := s.Disconnect(ctx)
		must(t, err)
	}
	wantValue(t, s, "a", "20", true)
	if _, err := s.Disconnect(ctx); !errors.Is(err, ErrUndoUnavailable) {
		t.Fatalf("err = %v, want ErrUndoUnavailable", err)
	}
}

func TestNetworkMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path, "regtest", 100)
	must(t, err)
	s.Close()
	if _, err := Open(path, "main", 100); err == nil {
		t.Fatal("opening a regtest database as main succeeded")
	}
}

func TestScan(t *testing.T) {
	s := openTest(t, 100)
	connect(t, s, 1, func(tx *BlockTx) {
		for _, k := range []string{"a/2", "a/1", "ab", "b/1", "a/3"} {
			must(t, tx.Put("test", k, []byte(k)))
		}
		must(t, tx.Delete("test", "a/3"))
		kvs, err := tx.Scan("test", "a/")
		must(t, err)
		if len(kvs) != 2 || kvs[0].Key != "a/1" || kvs[1].Key != "a/2" {
			t.Fatalf("scan = %+v", kvs)
		}
	})
	kvs, err := s.Scan(context.Background(), "test", "")
	must(t, err)
	if len(kvs) != 4 {
		t.Fatalf("full scan = %+v", kvs)
	}
}
