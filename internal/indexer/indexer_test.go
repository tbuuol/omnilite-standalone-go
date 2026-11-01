package indexer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

const exodusHash = "5c0bc5abc545e8c7387934507bcd20ea0fcb2ff2"

func p2pkhHex(h string) string { return "76a914" + h + "88ac" }

func classCHex(payload string) string {
	data := "6f6d6e69" + payload
	return fmt.Sprintf("6a%02x%s", len(data)/2, data)
}

func rep(b string) string { return strings.Repeat(b, 20) }

// tx renders a transaction the way litecoind prints it.
func tx(txid string, vin []string, vout ...[2]string) string {
	var ins []string
	for _, in := range vin {
		if in == "coinbase" {
			ins = append(ins, `{"coinbase":"03abcd"}`)
			continue
		}
		id, n, _ := strings.Cut(in, ":")
		ins = append(ins, fmt.Sprintf(`{"txid":%q,"vout":%s}`, id, n))
	}
	var outs []string
	for n, o := range vout {
		outs = append(outs, fmt.Sprintf(`{"value":%s,"n":%d,"scriptPubKey":{"hex":%q}}`, o[0], n, o[1]))
	}
	return fmt.Sprintf(`{"txid":%q,"vin":[%s],"vout":[%s]}`, txid, strings.Join(ins, ","), strings.Join(outs, ","))
}

type fakeNode struct {
	blocks map[string]string
	txs    map[string]string
	calls  int
}

func (n *fakeNode) GetBlock(_ context.Context, hash string) (*rpc.Block, error) {
	var b rpc.Block
	return &b, json.Unmarshal([]byte(n.blocks[hash]), &b)
}

func (n *fakeNode) GetRawTransaction(_ context.Context, txid string) (*rpc.Tx, error) {
	n.calls++
	s, ok := n.txs[txid]
	if !ok {
		return nil, &rpc.Error{Code: rpc.CodeInvalidAddressKey, Message: "No such mempool or blockchain transaction"}
	}
	var t rpc.Tx
	return &t, json.Unmarshal([]byte(s), &t)
}

func TestConnectBlock(t *testing.T) {
	a, b, r, seller := rep("a1"), rep("b2"), rep("c3"), rep("d4")
	send := "0000" + "0000" + "00000001" + "0000000000000064" // simple send 100 of property 1
	restricted := "0000" + "0002" + strings.Repeat("00", 12)

	node := &fakeNode{
		txs: map[string]string{
			"p1": tx("p1", []string{"coinbase"}, [2]string{"0.01000000", p2pkhHex(a)}, [2]string{"0.50000000", p2pkhHex(b)}),
		},
		blocks: map[string]string{"h": `{"hash":"h","height":2010500,"time":1650000000,"tx":[` + strings.Join([]string{
			tx("cb", []string{"coinbase"}, [2]string{"12.5", classCHex(send)}),
			tx("c1", []string{"p1:0", "p1:1"},
				[2]string{"0.00000000", classCHex(send)},
				[2]string{"0.00005460", p2pkhHex(r)},
				[2]string{"0.50990000", p2pkhHex(a)}),
			tx("n1", []string{"p1:1"}, [2]string{"0.4999", p2pkhHex(r)}),
			tx("x1", []string{"p1:0"}, [2]string{"0", classCHex(restricted)}),
			tx("d1", []string{"c1:2"},
				[2]string{"0.00005460", p2pkhHex(exodusHash)},
				[2]string{"0.25", p2pkhHex(seller)},
				[2]string{"0.2598", p2pkhHex(a)}),
		}, ",") + `]}`},
	}

	dec, err := omni.NewDecoder(&chain.Main)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), "main", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ix := &Indexer{Node: node, Dec: dec}
	ctx := context.Background()
	btx, err := st.Begin(ctx, store.Block{Height: 2010500, Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.ConnectBlock(ctx, btx, &rpc.BlockHeader{Hash: "h", Height: 2010500}); err != nil {
		t.Fatal(err)
	}
	if err := btx.Commit(); err != nil {
		t.Fatal(err)
	}

	get := func(txid string) *Record {
		v, ok, err := st.Get(ctx, NsTx, txid)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return nil
		}
		var rec Record
		if err := json.Unmarshal(v, &rec); err != nil {
			t.Fatal(err)
		}
		return &rec
	}
	addr := func(h string) string {
		var d omni.Destination
		raw, _ := hex.DecodeString(h)
		copy(d.Hash[:], raw)
		d.Kind = omni.DestKeyHash
		return d.Encode(&chain.Main)
	}

	c1 := get("c1")
	if c1 == nil || c1.Kind != KindOmni || c1.Class != "C" || c1.Sender != addr(a) || c1.Reference != addr(r) ||
		c1.Fee != 51_000_000-50_995_460 || c1.Payload != send || c1.Index != 1 || c1.Time != 1650000000 {
		t.Fatalf("c1 = %+v", c1)
	}
	d1 := get("d1")
	if d1 == nil || d1.Kind != KindDExPayment || d1.Sender != addr(a) || len(d1.Payments) != 2 ||
		d1.Payments[0].Address != addr(seller) || d1.Payments[0].Value != 25_000_000 {
		t.Fatalf("d1 = %+v", d1)
	}
	for _, id := range []string{"cb", "n1", "x1"} {
		if rec := get(id); rec != nil {
			t.Errorf("%s should not be recorded: %+v", id, rec)
		}
	}
	for idx, want := range map[int]string{1: "c1", 4: "d1"} {
		v, ok, _ := st.Get(ctx, NsBlockTx, BlockTxKey(2010500, idx))
		if !ok || string(v) != want {
			t.Errorf("blocktx %d = %q, want %s", idx, v, want)
		}
	}
	// The state engine ran: a has no tokens, so the send is invalid.
	if v, ok, _ := st.Get(ctx, state.NsResult, "c1"); !ok || !strings.Contains(string(v), `"code":-60025`) {
		t.Errorf("result c1 = %s", v)
	}
	// p1 is fetched once; c1 is found in the block itself.
	if node.calls != 1 {
		t.Errorf("getrawtransaction called %d times", node.calls)
	}
}

func TestLitoshi(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "12.5": 1_250_000_000, "0.00005460": 5460, "84000000.00000000": 8_400_000_000_000_000} {
		if got, err := rpc.Litoshi(json.Number(in)); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"1e-8", "0.000000001", ""} {
		if _, err := rpc.Litoshi(json.Number(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
