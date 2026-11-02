package wallet

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/mempool"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

var params = &chain.Main

// The secp256k1 generator, a valid compressed public key.
const genKey = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func dest(kind omni.DestKind, b byte) omni.Destination {
	d := omni.Destination{Kind: kind}
	for i := range d.Hash {
		d.Hash[i] = b
	}
	return d
}

var (
	aliceDest = dest(omni.DestScriptHash, 0xa1)
	alice     = aliceDest.Encode(params)
	bob       = dest(omni.DestScriptHash, 0xb2).Encode(params)
	carol     = dest(omni.DestKeyHash, 0xc3).Encode(params)
)

func TestGenesisTxid(t *testing.T) {
	const genesis = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff4d04ffff001d0104455468652054696d65732030332f4a616e2f32303039204368616e63656c6c6f72206f6e206272696e6b206f66207365636f6e64206261696c6f757420666f722062616e6b73ffffffff0100f2052a01000000434104678afdb0fe5548271967f1a67130b7105cd6a828e03909a67962e0ea1f61deb649f6bc3f4cef38c4f35504e51ec112de5c384df7ba0b8d578a4c702b6bf11d5fac00000000"
	tx, err := ParseTx(genesis)
	if err != nil {
		t.Fatal(err)
	}
	if got := tx.Txid(); got != "4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b" {
		t.Fatalf("txid = %s", got)
	}
	if tx.Hex() != genesis {
		t.Fatal("serialization differs")
	}
}

func TestWitnessRoundTrip(t *testing.T) {
	tx := NewTx()
	AddInput(tx, strings.Repeat("ab", 32), 1)
	AddInput(tx, strings.Repeat("cd", 32), 0)
	tx.Inputs[0].Script = []byte{0x16, 0, 0x14}
	tx.Inputs[0].Witness = [][]byte{{1, 2, 3}, {4}}
	AddOpReturn(tx, []byte{0, 0, 0, 0})
	back, err := ParseTx(tx.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if back.Hex() != tx.Hex() || back.Txid() != tx.Txid() || len(back.Inputs[1].Witness) != 0 {
		t.Fatalf("round trip differs: %s", back.Hex())
	}
	// A transaction without inputs is read in the legacy format.
	empty := NewTx()
	AddOpReturn(empty, []byte{1})
	if back, err := ParseTx(empty.Hex()); err != nil || back.Hex() != empty.Hex() {
		t.Fatalf("empty input list: %v", err)
	}
	if _, err := ParseTx(tx.Hex() + "00"); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestDust(t *testing.T) {
	key, _ := hex.DecodeString(genKey)
	cases := []struct {
		script []byte
		want   int64
	}{
		{omni.ScriptForDestination(dest(omni.DestKeyHash, 1)), 5460},
		{omni.ScriptForDestination(dest(omni.DestScriptHash, 1)), 5400},
		{omni.MultisigScript(1, [][]byte{key, key}), 6840},
		{omni.MultisigScript(1, [][]byte{key, key, key}), 7860},
		{[]byte{0x6a, 1, 0}, 0},
	}
	for _, c := range cases {
		if got := omniDust(c.script, 1000); got != c.want {
			t.Errorf("dust of %x = %d, want %d", c.script, got, c.want)
		}
	}
	if got := getFee(1000, 0); got != 0 {
		t.Errorf("fee of nothing = %d", got)
	}
	if got := getFee(1, 10); got != 1 {
		t.Errorf("minimum fee = %d", got)
	}
}

func TestAddChange(t *testing.T) {
	tx := NewTx()
	AddInput(tx, strings.Repeat("11", 32), 0)
	AddReference(tx, dest(omni.DestKeyHash, 2), 0, 1000)
	prevs := []Prevout{{Txid: strings.Repeat("11", 32), Vout: 0, Value: 100000}}
	AddChange(tx, prevs, aliceDest, 10000, 0, 1000)
	if len(tx.Outputs) != 2 || tx.Outputs[0].Value != 100000-5460-10000 {
		t.Fatalf("outputs = %+v", tx.Outputs)
	}
	// Unknown inputs leave the transaction unchanged.
	AddInput(tx, strings.Repeat("22", 32), 0)
	AddChange(tx, prevs, aliceDest, 10000, 0, 1000)
	if len(tx.Outputs) != 2 {
		t.Fatal("change added without all inputs")
	}
}

// --- sending through a fake node wallet ---

type fakeNode struct {
	height  int64
	coins   []map[string]any
	prev    map[string]*rpc.Tx
	sent    []*Tx
	signErr string
}

func reply(result any, v any) error {
	b, _ := json.Marshal(v)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(result)
}

func (n *fakeNode) Call(_ context.Context, method string, result any, params ...any) error {
	switch method {
	case "getnetworkinfo":
		return reply(result, map[string]any{"relayfee": json.Number("0.00001000")})
	case "estimatesmartfee":
		return reply(result, map[string]any{"errors": []string{"Insufficient data or no feerate found"}})
	case "getblockcount":
		return reply(result, n.height)
	case "listunspent":
		return reply(result, n.coins)
	case "getaddressinfo":
		return reply(result, map[string]any{"ismine": true, "embedded": map[string]any{"pubkey": genKey}})
	case "signrawtransactionwithwallet":
		if n.signErr != "" {
			return reply(result, map[string]any{"hex": params[0], "complete": false, "errors": []map[string]string{{"error": n.signErr}}})
		}
		tx, err := ParseTx(params[0].(string))
		if err != nil {
			return err
		}
		for i := range tx.Inputs {
			tx.Inputs[i].Script = append([]byte{0x16, 0, 0x14}, make([]byte, 20)...)
			tx.Inputs[i].Witness = [][]byte{make([]byte, 71), make([]byte, 33)}
		}
		return reply(result, map[string]any{"hex": tx.Hex(), "complete": true})
	case "sendrawtransaction":
		tx, err := ParseTx(params[0].(string))
		if err != nil {
			return err
		}
		n.sent = append(n.sent, tx)
		return reply(result, tx.Txid())
	}
	return fmt.Errorf("unexpected call %s", method)
}

func (n *fakeNode) GetBlock(context.Context, string) (*rpc.Block, error) {
	return nil, errors.New("not used")
}

func (n *fakeNode) GetRawTransaction(_ context.Context, txid string) (*rpc.Tx, error) {
	if t, ok := n.prev[txid]; ok {
		return t, nil
	}
	if strings.HasPrefix(txid, "0000") { // own unconfirmed change
		for _, s := range n.sent {
			if s.Txid() == txid {
				return rpcTx(s), nil
			}
		}
	}
	return nil, &rpc.Error{Code: -5, Message: "No such mempool or blockchain transaction"}
}

// coin gives the fake wallet a P2SH-P2WPKH coin of alice.
func (n *fakeNode) coin(i int, litoshi int64) { n.coinFor(aliceDest, i, litoshi) }

// coinFor gives the fake wallet a P2SH-P2WPKH coin of d.
func (n *fakeNode) coinFor(d omni.Destination, i int, litoshi int64) {
	id := fmt.Sprintf("%064x", 0xc0170000+i)
	script := hex.EncodeToString(omni.ScriptForDestination(d))
	n.coins = append(n.coins, map[string]any{
		"txid": id, "vout": 0, "address": d.Encode(params), "scriptPubKey": script,
		"redeemScript": "0014" + hex.EncodeToString(d.Hash[:]), "amount": json.Number(formatLTC(litoshi)),
		"confirmations": 10, "spendable": true, "safe": true,
	})
	out := rpc.TxOut{Value: json.Number(formatLTC(litoshi)), N: 0}
	out.ScriptPubKey.Hex = script
	n.prev[id] = &rpc.Tx{Txid: id, Vout: []rpc.TxOut{out}}
}

// issuance is a fixed issuance of 1000 divisible tokens.
func issuance() []byte {
	p := []byte{0, 0, 0, 50, 1, 0, 2, 0, 0, 0, 0}
	for _, s := range []string{"Games", "", "Coin", "", ""} {
		p = append(append(p, s...), 0)
	}
	return binary.BigEndian.AppendUint64(p, 1000_00000000)
}

func newWallet(t *testing.T) (*Wallet, *fakeNode) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), "main", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	height := params.OmniGenesis
	btx, err := st.Begin(ctx, store.Block{Height: height, Hash: strings.Repeat("00", 32), Time: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	s := state.New(btx, params, state.Options{})
	m, err := omni.Interpret(issuance(), alice, "", params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(&state.Tx{Txid: strings.Repeat("01", 32), Block: height, Sender: alice, Msg: m}); err != nil {
		t.Fatal(err)
	}
	if err := btx.Commit(); err != nil {
		t.Fatal(err)
	}
	node := &fakeNode{height: height, prev: map[string]*rpc.Tx{}}
	dec, err := omni.NewDecoder(params)
	if err != nil {
		t.Fatal(err)
	}
	w := &Wallet{Node: node, Dec: &indexer.Indexer{Node: node, Dec: dec}, Store: st, Pool: &mempool.Pool{},
		Params: params, FallbackFee: 10000}
	return w, node
}

func TestSendSimple(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	node.coin(1, 1_000_000)
	req := &Request{From: alice, To: bob, Payload: omni.PayloadSimpleSend(3, 400_00000000), Property: 3, Amount: 400_00000000}
	res, err := w.Send(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Class != omni.ClassC || len(node.sent) != 1 {
		t.Fatalf("class %v, sent %d", res.Class, len(node.sent))
	}
	tx := node.sent[0]
	// change to the sender first, then the payload, the reference last
	if len(tx.Outputs) != 3 || !bytes.Equal(tx.Outputs[0].Script, omni.ScriptForDestination(aliceDest)) ||
		tx.Outputs[1].Script[0] != 0x6a || tx.Outputs[2].Value != 5400 {
		t.Fatalf("outputs = %+v", tx.Outputs)
	}
	var out int64
	for _, o := range tx.Outputs {
		out += o.Value
	}
	if fee := 1_000_000 - out; fee != res.Fee || fee < 1000 || fee > 5000 {
		t.Fatalf("fee = %d, reported %d", fee, res.Fee)
	}
	if own := w.Pool.Own(); len(own) != 1 || own[0].Record.Txid != res.Txid || own[0].Record.Reference != bob {
		t.Fatalf("pending = %+v", own)
	}

	// The dry-run counts the pending send: 700 more would overdraw.
	node.coin(2, 1_000_000)
	req2 := &Request{From: alice, To: bob, Payload: omni.PayloadSimpleSend(3, 700_00000000)}
	_, err = w.Send(ctx, req2)
	var inv *InvalidError
	if !errors.As(err, &inv) || inv.Code != -60025 || len(node.sent) != 1 {
		t.Fatalf("overdraw: %v", err)
	}
	// 600 still fit.
	req2.Payload = omni.PayloadSimpleSend(3, 600_00000000)
	if _, err := w.Send(ctx, req2); err != nil {
		t.Fatal(err)
	}
}

func TestSendClassB(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	node.coin(1, 1_000_000)
	data := bytes.Repeat([]byte{0x42}, 100)
	res, err := w.Send(ctx, &Request{From: alice, Payload: omni.PayloadAnyData(data)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Class != omni.ClassB {
		t.Fatalf("class = %v", res.Class)
	}
	own := w.Pool.Own()
	if len(own) != 1 || own[0].Record.Class != "B" || own[0].Record.Reference != "" && own[0].Record.Reference != alice {
		t.Fatalf("record = %+v", own[0].Record)
	}
	if !strings.HasPrefix(own[0].Record.Payload, hex.EncodeToString(omni.PayloadAnyData(data))) {
		t.Fatalf("payload = %s", own[0].Record.Payload)
	}
	// 104 bytes need 4 packets: two multisig outputs, then Exodus.
	tx := node.sent[0]
	if n := len(tx.Outputs); n != 4 {
		t.Fatalf("outputs = %d", n)
	}
}

func TestSendFailures(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	req := &Request{From: alice, To: bob, Payload: omni.PayloadSimpleSend(3, 1)}

	var we *Error
	if _, err := w.Send(ctx, req); !errors.As(err, &we) || we.Code != ErrInputSelectFail {
		t.Fatalf("no coins: %v", err)
	}
	node.coin(1, 6000)
	if _, err := w.Send(ctx, req); !errors.As(err, &we) || we.Code != ErrCreateTx {
		t.Fatalf("too little: %v", err)
	}
	node.coin(2, 1_000_000)
	node.height++
	if _, err := w.Send(ctx, req); err == nil || !strings.Contains(err.Error(), "not in sync") {
		t.Fatalf("index behind: %v", err)
	}
	node.height--
	node.signErr = "Please enter the wallet passphrase with walletpassphrase first."
	if _, err := w.Send(ctx, req); !errors.As(err, &we) || !strings.Contains(we.Message, "walletpassphrase") {
		t.Fatalf("locked: %v", err)
	}
	// Nothing was broadcast.
	if len(node.sent) != 0 {
		t.Fatalf("sent %d", len(node.sent))
	}
	// Unknown property: invalid, caught before sending.
	node.signErr = ""
	req.Payload = omni.PayloadSimpleSend(99, 1)
	var inv *InvalidError
	if _, err := w.Send(ctx, req); !errors.As(err, &inv) || inv.Code != -60024 {
		t.Fatalf("unknown property: %v", err)
	}
}

func TestCreateFromP2PKH(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	// carol (L...) has a coin but no tokens: the result is invalid.
	script := hex.EncodeToString(omni.ScriptForDestination(dest(omni.DestKeyHash, 0xc3)))
	id := fmt.Sprintf("%064x", 7)
	node.coins = append(node.coins, map[string]any{"txid": id, "vout": 0, "address": carol, "scriptPubKey": script,
		"amount": json.Number("0.01000000"), "confirmations": 1, "spendable": true, "safe": true})
	out := rpc.TxOut{Value: "0.01000000"}
	out.ScriptPubKey.Hex = script
	node.prev[id] = &rpc.Tx{Txid: id, Vout: []rpc.TxOut{out}}
	var inv *InvalidError
	if _, err := w.Create(ctx, &Request{From: carol, To: bob, Payload: omni.PayloadSimpleSend(3, 1)}); !errors.As(err, &inv) || inv.Code != -60025 {
		t.Fatalf("carol: %v", err)
	}
}

func TestIssueThenGrant(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	node.coin(1, 1_000_000)
	node.coin(2, 1_000_000)
	// A long name forces class B.
	info := &omni.PropertyInfo{Ecosystem: 1, Type: 2, Category: "Games", Name: strings.Repeat("N", 90)}
	res, err := w.Send(ctx, &Request{From: alice, Payload: omni.PayloadIssuanceManaged(info)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Class != omni.ClassB {
		t.Fatalf("class = %v", res.Class)
	}
	// Property 4 exists only in the mempool; the dry-run applies the
	// pending issuance first, so the grant is valid.
	if _, err := w.Send(ctx, &Request{From: alice, To: bob, Payload: omni.PayloadGrant(4, 50, "")}); err != nil {
		t.Fatal(err)
	}
}

func TestDExTrade(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	bobDest := dest(omni.DestScriptHash, 0xb2)
	node.coin(1, 1_000_000)
	node.coinFor(bobDest, 2, 2_00000000)

	// alice offers 100 tokens for 1 LTC, minimum accept fee 0.0001 LTC.
	offer := omni.PayloadDExSell(3, 100_00000000, 1_00000000, 10, 10000, 1)
	if _, err := w.Send(ctx, &Request{From: alice, Payload: offer, Property: 3, Amount: 100_00000000}); err != nil {
		t.Fatal(err)
	}
	// bob reserves 40 tokens; the offer is still unconfirmed.
	res, err := w.Send(ctx, &Request{From: bob, To: alice, Payload: omni.PayloadDExAccept(3, 40_00000000), MinFee: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fee < 10000 {
		t.Fatalf("accept fee %d below the offer minimum", res.Fee)
	}
	// Paying for another seller buys nothing and is refused.
	var ref *RefusedError
	if _, err := w.Send(ctx, &Request{From: bob, Pay: &Payment{Seller: carol, Property: 3, Amount: 40000000}}); !errors.As(err, &ref) {
		t.Fatalf("payment to carol: %v", err)
	}
	// bob pays 0.4 LTC for the 40 tokens.
	res, err = w.Send(ctx, &Request{From: bob, Pay: &Payment{Seller: alice, Property: 3, Amount: 40000000}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Class != omni.ClassA {
		t.Fatalf("class = %v", res.Class)
	}
	pay := node.sent[len(node.sent)-1]
	exodus, _ := omni.DecodeAddress(params.Exodus, params)
	if len(pay.Outputs) != 3 || !bytes.Equal(pay.Outputs[1].Script, omni.ScriptForDestination(exodus)) ||
		pay.Outputs[2].Value != 40000000 || !bytes.Equal(pay.Outputs[2].Script, omni.ScriptForDestination(aliceDest)) {
		t.Fatalf("payment outputs = %+v", pay.Outputs)
	}
	own := w.Pool.Own()
	if last := own[len(own)-1]; last.Msg != nil || last.Record.Kind != indexer.KindDExPayment || last.Record.Sender != bob {
		t.Fatalf("pending payment = %+v", last.Record)
	}
	// The accept is fully paid: a second payment buys nothing.
	if _, err := w.Send(ctx, &Request{From: bob, Pay: &Payment{Seller: alice, Property: 3, Amount: 10000000}}); !errors.As(err, &ref) {
		t.Fatalf("second payment: %v", err)
	}
}

func TestOpenAccepts(t *testing.T) {
	ctx := context.Background()
	w, node := newWallet(t)
	// Block 2: alice's offer and bob's accept, confirmed.
	height := params.OmniGenesis + 1
	btx, err := w.Store.Begin(ctx, store.Block{Height: height, Hash: strings.Repeat("02", 32), PrevHash: strings.Repeat("00", 32), Time: 1700000600})
	if err != nil {
		t.Fatal(err)
	}
	s := state.New(btx, params, state.Options{})
	for i, x := range []struct {
		sender, ref string
		payload     []byte
	}{
		{alice, "", omni.PayloadDExSell(3, 100_00000000, 1_00000000, 10, 0, 1)},
		{bob, alice, omni.PayloadDExAccept(3, 40_00000000)},
	} {
		m, err := omni.Interpret(x.payload, x.sender, x.ref, params)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Apply(&state.Tx{Txid: fmt.Sprintf("%064x", 100+i), Block: height, Index: i, Sender: x.sender, Fee: 1000, Msg: m})
		if err != nil || !res.Valid {
			t.Fatalf("tx %d: %+v %v", i, res, err)
		}
	}
	if err := btx.Commit(); err != nil {
		t.Fatal(err)
	}
	node.height = height
	list, err := w.OpenAccepts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Seller != alice || list[0].Buyer != bob || list[0].AmountToPay != 40000000 ||
		list[0].BlocksLeft != 10 || !list[0].MineBuyer {
		t.Fatalf("accepts = %+v", list)
	}
}
