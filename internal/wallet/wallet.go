// Package wallet builds, checks and sends Omni transactions with the
// wallet of litecoind. The node keeps the keys and signs; this package
// selects the sender's coins, lays out the outputs like OmniLite's
// WalletTxBuilder and runs every transaction through the own state before
// it is broadcast, so an Omni-invalid transaction never costs a fee.
package wallet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/mempool"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// Error codes of OmniLite's wallet functions (errors.h). They are returned
// as JSON-RPC error codes, as omni_send* do.
const (
	ErrWalletAccess    = -205
	ErrInputSelectFail = -206
	ErrCreateTx        = -211
	ErrInputsInvalid   = -212
	ErrCommitTx        = -213
	ErrRedeemBadValid  = -223
	ErrEncoding        = -250
)

var errorText = map[int]string{
	ErrWalletAccess:    "Error with wallet object",
	ErrInputSelectFail: "Error with selected inputs for the send transaction",
	ErrCreateTx:        "Error creating transaction (wallet may be locked or fees may not be sufficient)",
	ErrInputsInvalid:   "Error choosing inputs for the send transaction",
	ErrCommitTx:        "Error committing transaction",
	ErrRedeemBadValid:  "Error validating redemption address",
	ErrEncoding:        "Error encoding transaction",
}

// Error is a failure with a JSON-RPC error code.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(code int, detail string) *Error {
	msg := errorText[code]
	if detail != "" {
		msg += ": " + detail
	}
	return &Error{Code: code, Message: msg}
}

// InvalidError reports that the dry-run found the transaction
// Omni-invalid. Code is the interpretPacket result.
type InvalidError struct {
	Code int
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("transaction would be invalid (%d)", e.Code)
}

// Node is the RPC access to litecoind and its wallet.
type Node interface {
	Call(ctx context.Context, method string, result any, params ...any) error
}

// Decoder decodes a transaction like the indexer does.
type Decoder interface {
	DecodeTx(ctx context.Context, t *rpc.Tx) (*indexer.Record, *omni.Message, error)
}

// Pool tracks the transactions this program sent.
type Pool interface {
	Own() []*mempool.Entry
	Sent(e *mempool.Entry, property uint32, amount int64)
}

type Wallet struct {
	Node   Node
	Dec    Decoder
	Store  *store.Store
	Pool   Pool
	Params *chain.Params
	State  state.Options
	// FallbackFee is the fee rate in litoshi per kB when the node has no
	// estimate (-fallbackfee).
	FallbackFee int64
	Log         *slog.Logger

	// mu serializes sends, so two sends never pick the same coins.
	mu sync.Mutex
}

// Request describes an Omni transaction to build (WalletTxBuilder).
type Request struct {
	From string
	// To receives the reference output; empty for none.
	To string
	// Redeem is the address or hex key put into Class B multisig outputs;
	// empty for the sender.
	Redeem string
	// ReferenceAmount is the reference output's value; 0 for the dust minimum.
	ReferenceAmount int64
	Payload         []byte
	// Property and Amount are the pending debit (PendingAdd); 0 for none.
	Property uint32
	Amount   int64
}

// Result is a built transaction.
type Result struct {
	Txid  string
	Hex   string
	Fee   int64
	Class omni.Class
}

// Send builds, checks, signs and broadcasts the transaction.
func (w *Wallet) Send(ctx context.Context, req *Request) (*Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := w.build(ctx, req)
	if err != nil {
		return nil, err
	}
	rec, msg, err := w.dryRun(ctx, req, b)
	if err != nil {
		return nil, err
	}
	signed, err := w.sign(ctx, b.tx)
	if err != nil {
		return nil, err
	}
	var txid string
	if err := w.Node.Call(ctx, "sendrawtransaction", &txid, signed.Hex()); err != nil {
		return nil, err
	}
	rec.Txid = txid
	w.Pool.Sent(&mempool.Entry{Record: rec, Msg: msg, Seen: time.Now()}, req.Property, req.Amount)
	if w.Log != nil {
		w.Log.Info("omni transaction sent", "txid", txid, "type", msg.Type, "from", req.From, "fee", b.fee)
	}
	return &Result{Txid: txid, Hex: signed.Hex(), Fee: b.fee, Class: b.class}, nil
}

// Create builds and checks the transaction like Send, but returns it
// unsigned instead of broadcasting it.
func (w *Wallet) Create(ctx context.Context, req *Request) (*Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := w.build(ctx, req)
	if err != nil {
		return nil, err
	}
	if _, _, err := w.dryRun(ctx, req, b); err != nil {
		return nil, err
	}
	return &Result{Txid: b.tx.Txid(), Hex: b.tx.Hex(), Fee: b.fee, Class: b.class}, nil
}

type built struct {
	tx    *Tx
	fee   int64
	class omni.Class
}

type unspent struct {
	Txid          string      `json:"txid"`
	Vout          uint32      `json:"vout"`
	Address       string      `json:"address"`
	ScriptPubKey  string      `json:"scriptPubKey"`
	RedeemScript  string      `json:"redeemScript"`
	Amount        json.Number `json:"amount"`
	Confirmations int64       `json:"confirmations"`
	Spendable     bool        `json:"spendable"`
	Safe          bool        `json:"safe"`
}

type addressInfo struct {
	IsMine   bool   `json:"ismine"`
	PubKey   string `json:"pubkey"`
	Embedded *struct {
		PubKey string `json:"pubkey"`
	} `json:"embedded"`
}

func (a *addressInfo) pubKey() string {
	if a.PubKey == "" && a.Embedded != nil {
		return a.Embedded.PubKey
	}
	return a.PubKey
}

// Fees returns the fee rate for new transactions and the relay fee, in
// litoshi per kB. The fee rate follows the wallet's minimum fee: the
// estimate for 6 blocks (or the fallback), at least the relay fee.
func (w *Wallet) Fees(ctx context.Context) (feeRate, relayFee int64, err error) {
	var ni struct {
		RelayFee json.Number `json:"relayfee"`
	}
	if err := w.Node.Call(ctx, "getnetworkinfo", &ni); err != nil {
		return 0, 0, err
	}
	if relayFee, err = rpc.Litoshi(ni.RelayFee); err != nil {
		return 0, 0, err
	}
	var est struct {
		FeeRate json.Number `json:"feerate"`
	}
	feeRate = w.FallbackFee
	if err := w.Node.Call(ctx, "estimatesmartfee", &est, 6); err == nil && est.FeeRate != "" {
		if r, err := rpc.Litoshi(est.FeeRate); err == nil && r > 0 {
			feeRate = r
		}
	}
	return max(feeRate, relayFee), relayFee, nil
}

// PubKey returns the public key of a wallet address, or the key itself if
// key is hex (AddressToPubKey).
func (w *Wallet) PubKey(ctx context.Context, key string) ([]byte, error) {
	if _, err := omni.DecodeAddress(key, w.Params); err == nil {
		var info addressInfo
		if err := w.Node.Call(ctx, "getaddressinfo", &info, key); err != nil {
			return nil, err
		}
		k, err := hex.DecodeString(info.pubKey())
		if err != nil || !omni.FullyValidPubKey(k) {
			return nil, fail(ErrRedeemBadValid, "no public key in the wallet for "+key)
		}
		return k, nil
	}
	k, err := hex.DecodeString(key)
	if err != nil || !omni.FullyValidPubKey(k) {
		return nil, fail(ErrRedeemBadValid, "invalid redemption key "+key)
	}
	return k, nil
}

func (w *Wallet) build(ctx context.Context, req *Request) (*built, error) {
	from, err := omni.DecodeAddress(req.From, w.Params)
	if err != nil {
		return nil, fail(ErrInputsInvalid, "invalid sender address")
	}
	feeRate, relayFee, err := w.Fees(ctx)
	if err != nil {
		return nil, err
	}
	var height int64
	if err := w.Node.Call(ctx, "getblockcount", &height); err != nil {
		return nil, err
	}

	// SelectCoins: spendable coins of the sender, up to the fee for 20 kB
	// plus the reference amount. Unconfirmed coins count if the wallet
	// trusts them (its own change).
	var coins []unspent
	if err := w.Node.Call(ctx, "listunspent", &coins, 0, 9999999, []string{req.From}); err != nil {
		if rpc.IsCode(err, rpc.CodeMethodNotFound) {
			return nil, fail(ErrWalletAccess, "the node has no wallet")
		}
		return nil, err
	}
	sort.SliceStable(coins, func(i, j int) bool {
		if coins[i].Confirmations != coins[j].Confirmations {
			return coins[i].Confirmations > coins[j].Confirmations
		}
		if coins[i].Txid != coins[j].Txid {
			return coins[i].Txid < coins[j].Txid
		}
		return coins[i].Vout < coins[j].Vout
	})
	pubKeySize := 33
	if from.Kind == omni.DestKeyHash {
		var info addressInfo
		if err := w.Node.Call(ctx, "getaddressinfo", &info, req.From); err == nil && len(info.PubKey) == 130 {
			pubKeySize = 65
		}
	}
	senderScript := omni.ScriptForDestination(from)
	maxTotal := 20*feeRate + max(req.ReferenceAmount, 0)
	t := NewTx()
	t.LockTime = uint32(height) // anti fee sniping, as the wallet does
	var kinds []inputKind
	var total int64
	for _, c := range coins {
		if !c.Spendable || !c.Safe {
			continue
		}
		script, err := hex.DecodeString(c.ScriptPubKey)
		if err != nil || !strings.EqualFold(c.ScriptPubKey, hex.EncodeToString(senderScript)) {
			continue
		}
		value, err := rpc.Litoshi(c.Amount)
		if err != nil {
			return nil, err
		}
		if value < economicThreshold(script, relayFee, feeRate) {
			continue
		}
		var kind inputKind
		if from.Kind == omni.DestKeyHash {
			kind = p2pkhInput(pubKeySize)
		} else if rs := strings.ToLower(c.RedeemScript); len(rs) == 44 && strings.HasPrefix(rs, "0014") {
			kind = p2shP2WPKHInput
		} else {
			continue // only P2SH-P2WPKH is supported behind an M address
		}
		t.Inputs = append(t.Inputs, TxIn{Txid: c.Txid, Vout: c.Vout, Sequence: 0xfffffffe})
		kinds = append(kinds, kind)
		total += value
		if total >= maxTotal {
			break
		}
	}
	if len(t.Inputs) == 0 {
		return nil, fail(ErrInputSelectFail, "no spendable coins at "+req.From)
	}

	// Data outputs.
	b := &built{tx: t, class: omni.ClassC}
	if omni.UseClassC(len(req.Payload)) {
		script, err := omni.ClassCScript(req.Payload)
		if err != nil {
			return nil, fail(ErrEncoding, err.Error())
		}
		t.Outputs = append(t.Outputs, TxOut{Value: 0, Script: script})
	} else {
		b.class = omni.ClassB
		redeem := req.Redeem
		if redeem == "" {
			redeem = req.From
		}
		key, err := w.PubKey(ctx, redeem)
		if err != nil {
			return nil, err
		}
		scripts, err := omni.ClassBScripts(req.From, key, req.Payload)
		if err != nil {
			return nil, fail(ErrEncoding, err.Error())
		}
		exodus, err := omni.DecodeAddress(w.Params.Exodus, w.Params)
		if err != nil {
			return nil, err
		}
		for _, s := range append(scripts, omni.ScriptForDestination(exodus)) {
			t.Outputs = append(t.Outputs, TxOut{Value: omniDust(s, relayFee), Script: s})
		}
	}
	// The reference output comes last.
	if req.To != "" {
		to, err := omni.DecodeAddress(req.To, w.Params)
		if err != nil {
			return nil, fail(ErrCreateTx, "invalid reference address")
		}
		s := omni.ScriptForDestination(to)
		v := req.ReferenceAmount
		if v <= 0 {
			v = omniDust(s, relayFee)
		}
		t.Outputs = append(t.Outputs, TxOut{Value: v, Script: s})
	}

	var out int64
	for _, o := range t.Outputs {
		out += o.Value
	}
	// Change back to the sender, first like omni_createrawtx_change; the
	// reference rules skip the first output to the sender.
	withChange := append([]TxOut{{Script: senderScript}}, t.Outputs...)
	fee := getFee(feeRate, virtualSize(&Tx{Outputs: withChange}, kinds))
	change := total - out - fee
	if change >= omniDust(senderScript, relayFee) {
		withChange[0].Value = change
		t.Outputs = withChange
	} else {
		fee = total - out
		if need := getFee(feeRate, virtualSize(t, kinds)); fee < need {
			return nil, fail(ErrCreateTx, fmt.Sprintf("insufficient funds at %s: %s LTC available, %s LTC needed",
				req.From, formatLTC(total), formatLTC(out+need)))
		}
	}
	b.fee = fee
	return b, nil
}

// sign has the node's wallet sign all inputs.
func (w *Wallet) sign(ctx context.Context, t *Tx) (*Tx, error) {
	var r struct {
		Hex      string `json:"hex"`
		Complete bool   `json:"complete"`
		Errors   []struct {
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := w.Node.Call(ctx, "signrawtransactionwithwallet", &r, t.Hex()); err != nil {
		return nil, err
	}
	if !r.Complete {
		msg := "signing incomplete"
		if len(r.Errors) > 0 {
			msg = r.Errors[0].Error
		}
		return nil, fail(ErrCreateTx, msg)
	}
	signed, err := ParseTx(r.Hex)
	if err != nil {
		return nil, err
	}
	// The wallet must not have changed anything but the signatures.
	if len(signed.Inputs) != len(t.Inputs) || len(signed.Outputs) != len(t.Outputs) {
		return nil, errors.New("signed transaction differs from the built one")
	}
	for i, in := range signed.Inputs {
		if in.Txid != t.Inputs[i].Txid || in.Vout != t.Inputs[i].Vout {
			return nil, errors.New("signed transaction differs from the built one")
		}
	}
	for i, o := range signed.Outputs {
		if o.Value != t.Outputs[i].Value || hex.EncodeToString(o.Script) != hex.EncodeToString(t.Outputs[i].Script) {
			return nil, errors.New("signed transaction differs from the built one")
		}
	}
	return signed, nil
}

func formatLTC(v int64) string { return fmt.Sprintf("%d.%08d", v/100000000, v%100000000) }

// rpcTx converts t to the decoded form the indexer reads.
func rpcTx(t *Tx) *rpc.Tx {
	r := &rpc.Tx{Txid: t.Txid()}
	for _, in := range t.Inputs {
		r.Vin = append(r.Vin, rpc.TxIn{Txid: in.Txid, Vout: in.Vout})
	}
	for i, o := range t.Outputs {
		out := rpc.TxOut{Value: json.Number(formatLTC(o.Value)), N: uint32(i)}
		out.ScriptPubKey.Hex = hex.EncodeToString(o.Script)
		r.Vout = append(r.Vout, out)
	}
	return r
}
