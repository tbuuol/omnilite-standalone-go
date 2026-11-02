package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
	"github.com/tbuuol/omnilite-standalone/internal/wallet"
)

// Calls that need the node or its wallet, not only the index (rpctx.cpp,
// rpcrawtx.cpp). They run outside of a snapshot, since building and
// signing take node round trips.

const codeDeserialization = -22

type nodeHandler func(ctx context.Context, s *Server, a args) (any, error)

var nodeMethods map[string]nodeHandler

// walletMethods need wallet=1.
var walletMethods = map[string]bool{
	"omni_send": true, "omni_sendall": true, "omni_sendnonfungible": true,
	"omni_sendanydata": true, "omni_sendrawtx": true,
}

func init() {
	nodeMethods = map[string]nodeHandler{
		"omni_send":                  send,
		"omni_sendall":               sendAll,
		"omni_sendnonfungible":       sendNonFungible,
		"omni_sendanydata":           sendAnyData,
		"omni_sendrawtx":             sendRawTx,
		"omni_createrawtx_opreturn":  createRawTxOpReturn,
		"omni_createrawtx_multisig":  createRawTxMultisig,
		"omni_createrawtx_input":     createRawTxInput,
		"omni_createrawtx_reference": createRawTxReference,
		"omni_createrawtx_change":    createRawTxChange,
	}
	methods["omni_createpayload_simplesend"] = createPayloadSimpleSend
	methods["omni_createpayload_sendall"] = createPayloadSendAll
	methods["omni_createpayload_sendnonfungible"] = createPayloadSendNonFungible
	methods["omni_createpayload_anydata"] = createPayloadAnyData
	registerManage()
}

// read runs fn on a snapshot query.
func (s *Server) read(ctx context.Context, fn func(q *query) error) error {
	return s.Store.Read(ctx, func(snap *store.Snapshot) error {
		return fn(newQuery(snap, s.Params, s.Pool))
	})
}

// walletError turns wallet failures into JSON-RPC errors.
func walletError(err error) error {
	var we *wallet.Error
	var inv *wallet.InvalidError
	switch {
	case errors.As(err, &we):
		return errorf(we.Code, we.Message)
	case errors.As(err, &inv):
		return errorf(codeType, "Transaction would be invalid and was not sent: "+errorString(inv.Code))
	}
	return err
}

// --- argument parsing (rpcvalues.cpp) ---

// amount mirrors ParseAmount.
func (a args) amount(i int, divisible bool) (int64, error) {
	s, err := a.str(i)
	if err != nil {
		return 0, err
	}
	n := strToInt64(s, divisible)
	if n < 1 {
		return 0, errorf(codeType, "Invalid amount")
	}
	return n, nil
}

// optString is the string at i, or "" if missing or null.
func (a args) optString(i int) (string, error) {
	if !a.has(i) {
		return "", nil
	}
	return a.str(i)
}

// addressOrEmpty mirrors ParseAddressOrEmpty.
func (q *query) addressOrEmpty(a args, i int) (string, error) {
	s, err := a.optString(i)
	if err != nil || s == "" {
		return "", err
	}
	return q.address(a, i)
}

// hexV mirrors ParseHexV.
func (a args) hexV(i int, name string) ([]byte, error) {
	s, err := a.str(i)
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(s) == 0 {
		return nil, errorf(codeInvalidParam, name+" must be hexadecimal string (not '"+s+"')")
	}
	return b, nil
}

// ecosystem mirrors ParseEcosystem.
func (a args) ecosystem(i int) (uint8, error) {
	n, err := a.int64(i)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > 2 {
		return 0, errorf(codeInvalidParam, "Invalid ecosystem (1 = main, 2 = test only)")
	}
	return uint8(n), nil
}

// coinAmount mirrors AmountFromValue: a number or string of LTC.
func (a args) coinAmount(i int) (int64, error) {
	return amountFromJSON(a[i])
}

func amountFromJSON(raw json.RawMessage) (int64, error) {
	var v json.Number
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, errorf(codeType, "Invalid amount")
		}
		v = json.Number(s)
	} else {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return 0, errorf(codeType, "Amount is not a number or string")
		}
	}
	n, err := rpc.Litoshi(v)
	if err != nil {
		return 0, errorf(codeType, "Invalid amount")
	}
	if n < 0 || n > 84_000_000*100_000_000 {
		return 0, errorf(codeType, "Amount out of range")
	}
	return n, nil
}

// rawTx mirrors ParseMutableTransaction: an empty string is a new transaction.
func (a args) rawTx(i int) (*wallet.Tx, error) {
	s, err := a.optString(i)
	if err != nil {
		return nil, err
	}
	t, err := wallet.ParseTx(s)
	if err != nil {
		return nil, errorf(codeDeserialization, "Transaction deserialization failed")
	}
	return t, nil
}

// outputIndex mirrors ParseOutputIndex.
func (a args) outputIndex(i int) (uint32, error) {
	n, err := a.int64(i)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errorf(codeDeserialization, "Vout index must be positive")
	}
	return uint32(n), nil
}

// --- requirements (rpcrequirements.cpp) ---

// requireBalance mirrors RequireBalance.
func (q *query) requireBalance(addr string, id uint32, amount int64) error {
	bal, err := q.st.Balance(addr, id, state.Balance)
	if err != nil {
		return err
	}
	if bal < amount {
		return errorf(codeType, "Sender has insufficient balance")
	}
	if q.pool != nil && bal-q.pool.PendingDebit(addr, id) < amount {
		return errorf(codeType, "Sender has insufficient balance (due to pending transactions)")
	}
	return nil
}

func requireSaneReferenceAmount(n int64) error {
	if n > 1_000_000 {
		return errorf(codeType, "Reference amount higher is than 0.01 LTC")
	}
	return nil
}

func requireSaneNonFungibleRange(start, end int64) error {
	switch {
	case start <= 0:
		return errorf(codeInvalidParam, "Unique range start value must not be zero or negative")
	case end <= 0:
		return errorf(codeInvalidParam, "Unique range end value must not be zero or negative")
	case start > end:
		return errorf(codeInvalidParam, "Unique range start value must be higher than non-fungible range end value")
	}
	return nil
}

// --- payloads (rpcpayload.cpp) ---

func createPayloadSimpleSend(q *query, a args) (any, error) {
	if err := a.count(2, 2, "omni_createpayload_simplesend propertyid \"amount\""); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	div, err := q.st.Divisible(id)
	if err != nil {
		return nil, err
	}
	amount, err := a.amount(1, div)
	if err != nil {
		return nil, err
	}
	return hex.EncodeToString(omni.PayloadSimpleSend(id, uint64(amount))), nil
}

func createPayloadSendAll(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_createpayload_sendall ecosystem"); err != nil {
		return nil, err
	}
	eco, err := a.ecosystem(0)
	if err != nil {
		return nil, err
	}
	return hex.EncodeToString(omni.PayloadSendAll(eco)), nil
}

func createPayloadSendNonFungible(q *query, a args) (any, error) {
	if err := a.count(3, 3, "omni_createpayload_sendnonfungible propertyid tokenstart tokenend"); err != nil {
		return nil, err
	}
	id, err := a.propertyID(0)
	if err != nil {
		return nil, err
	}
	start, err := a.int64(1)
	if err != nil {
		return nil, err
	}
	end, err := a.int64(2)
	if err != nil {
		return nil, err
	}
	if err := requireSaneNonFungibleRange(start, end); err != nil {
		return nil, err
	}
	return hex.EncodeToString(omni.PayloadSendNonFungible(id, uint64(start), uint64(end))), nil
}

func createPayloadAnyData(q *query, a args) (any, error) {
	if err := a.count(1, 1, "omni_createpayload_anydata \"data\""); err != nil {
		return nil, err
	}
	data, err := a.hexV(0, "data")
	if err != nil {
		return nil, err
	}
	return hex.EncodeToString(omni.PayloadAnyData(data)), nil
}

// --- sending (rpctx.cpp) ---

// commit sends the request through the node's wallet and returns the txid.
func (s *Server) commit(ctx context.Context, req *wallet.Request) (any, error) {
	res, err := s.Wallet.Send(ctx, req)
	if err != nil {
		return nil, walletError(err)
	}
	return res.Txid, nil
}

// redeemAndReference parses the optional redeemaddress and referenceamount
// at i and i+1.
func (q *query) redeemAndReference(a args, i int, req *wallet.Request) error {
	if s, err := a.optString(i); err != nil {
		return err
	} else if s != "" {
		if req.Redeem, err = q.address(a, i); err != nil {
			return err
		}
	}
	if len(a) > i+1 {
		n, err := a.amount(i+1, true)
		if err != nil {
			return err
		}
		req.ReferenceAmount = n
	}
	return requireSaneReferenceAmount(req.ReferenceAmount)
}

func send(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(4, 6, "omni_send \"fromaddress\" \"toaddress\" propertyid \"amount\" ( \"redeemaddress\" \"referenceamount\" )"); err != nil {
		return nil, err
	}
	req := &wallet.Request{}
	err := s.read(ctx, func(q *query) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if req.To, err = q.address(a, 1); err != nil {
			return err
		}
		if req.Property, err = a.propertyID(2); err != nil {
			return err
		}
		div, err := q.st.Divisible(req.Property)
		if err != nil {
			return err
		}
		if req.Amount, err = a.amount(3, div); err != nil {
			return err
		}
		if err := q.redeemAndReference(a, 4, req); err != nil {
			return err
		}
		if _, err := q.requireExisting(req.Property); err != nil {
			return err
		}
		return q.requireBalance(req.From, req.Property, req.Amount)
	})
	if err != nil {
		return nil, err
	}
	req.Payload = omni.PayloadSimpleSend(req.Property, uint64(req.Amount))
	return s.commit(ctx, req)
}

func sendAll(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(3, 5, "omni_sendall \"fromaddress\" \"toaddress\" ecosystem ( \"redeemaddress\" \"referenceamount\" )"); err != nil {
		return nil, err
	}
	req := &wallet.Request{}
	var eco uint8
	err := s.read(ctx, func(q *query) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if req.To, err = q.address(a, 1); err != nil {
			return err
		}
		if eco, err = a.ecosystem(2); err != nil {
			return err
		}
		return q.redeemAndReference(a, 3, req)
	})
	if err != nil {
		return nil, err
	}
	req.Payload = omni.PayloadSendAll(eco)
	return s.commit(ctx, req)
}

func sendNonFungible(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(5, 7, "omni_sendnonfungible \"fromaddress\" \"toaddress\" propertyid tokenstart tokenend ( \"redeemaddress\" \"referenceamount\" )"); err != nil {
		return nil, err
	}
	req := &wallet.Request{}
	var start, end int64
	err := s.read(ctx, func(q *query) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if req.To, err = q.address(a, 1); err != nil {
			return err
		}
		if req.Property, err = a.propertyID(2); err != nil {
			return err
		}
		if start, err = a.int64(3); err != nil {
			return err
		}
		if end, err = a.int64(4); err != nil {
			return err
		}
		req.Amount = end - start + 1
		if err := q.redeemAndReference(a, 5, req); err != nil {
			return err
		}
		if _, err := q.requireExisting(req.Property); err != nil {
			return err
		}
		if err := q.requireBalance(req.From, req.Property, req.Amount); err != nil {
			return err
		}
		// RequireNonFungibleTokenOwner
		first, err := q.st.NFTOwner(req.Property, start)
		if err != nil {
			return err
		}
		last, err := q.st.NFTOwner(req.Property, end)
		if err != nil {
			return err
		}
		contiguous, err := q.st.NFTContiguous(req.Property, start, end)
		if err != nil {
			return err
		}
		if first != req.From || last != req.From || !contiguous {
			return errorf(codeType, "Sender does not own the range")
		}
		return requireSaneNonFungibleRange(start, end)
	})
	if err != nil {
		return nil, err
	}
	req.Payload = omni.PayloadSendNonFungible(req.Property, uint64(start), uint64(end))
	return s.commit(ctx, req)
}

func sendAnyData(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(2, 3, "omni_sendanydata \"fromaddress\" \"data\" ( \"toaddress\" )"); err != nil {
		return nil, err
	}
	req := &wallet.Request{}
	err := s.read(ctx, func(q *query) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		data, err := a.hexV(1, "data")
		if err != nil {
			return err
		}
		req.Payload = omni.PayloadAnyData(data)
		req.To, err = q.addressOrEmpty(a, 2)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.commit(ctx, req)
}

func sendRawTx(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(2, 5, "omni_sendrawtx \"fromaddress\" \"rawtransaction\" ( \"referenceaddress\" \"redeemaddress\" \"referenceamount\" )"); err != nil {
		return nil, err
	}
	req := &wallet.Request{}
	err := s.read(ctx, func(q *query) error {
		var err error
		if req.From, err = q.address(a, 0); err != nil {
			return err
		}
		if req.Payload, err = a.hexV(1, "raw transaction"); err != nil {
			return err
		}
		if req.To, err = q.addressOrEmpty(a, 2); err != nil {
			return err
		}
		if req.Redeem, err = q.addressOrEmpty(a, 3); err != nil {
			return err
		}
		if len(a) > 4 {
			req.ReferenceAmount, err = a.amount(4, true)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.commit(ctx, req)
}

// --- raw transactions (rpcrawtx.cpp) ---

func (s *Server) relayFee(ctx context.Context) (int64, error) {
	if s.Node == nil {
		return 0, errors.New("no node connection")
	}
	return wallet.RelayFee(ctx, s.Node)
}

func createRawTxOpReturn(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(2, 2, "omni_createrawtx_opreturn \"rawtx\" \"payload\""); err != nil {
		return nil, err
	}
	t, err := a.rawTx(0)
	if err != nil {
		return nil, err
	}
	payload, err := a.hexV(1, "payload")
	if err != nil {
		return nil, err
	}
	wallet.AddOpReturn(t, payload)
	return t.Hex(), nil
}

func createRawTxMultisig(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(4, 4, "omni_createrawtx_multisig \"rawtx\" \"payload\" \"seed\" \"redeemkey\""); err != nil {
		return nil, err
	}
	t, err := a.rawTx(0)
	if err != nil {
		return nil, err
	}
	payload, err := a.hexV(1, "payload")
	if err != nil {
		return nil, err
	}
	var seed string
	if err := s.read(ctx, func(q *query) error {
		seed, err = q.addressOrEmpty(a, 2)
		return err
	}); err != nil {
		return nil, err
	}
	keyArg, err := a.str(3)
	if err != nil {
		return nil, err
	}
	// ParsePubKeyOrAddress: addresses need the wallet.
	var key []byte
	if s.Wallet != nil {
		key, err = s.Wallet.PubKey(ctx, keyArg)
	} else if key, err = hex.DecodeString(keyArg); err == nil && !omni.FullyValidPubKey(key) {
		err = errors.New("invalid key")
	}
	if err != nil || key == nil {
		return nil, errorf(codeInvalidAddress, "Invalid redemption key or address")
	}
	exodus, err := omni.DecodeAddress(s.Params.Exodus, s.Params)
	if err != nil {
		return nil, err
	}
	relay, err := s.relayFee(ctx)
	if err != nil {
		return nil, err
	}
	wallet.AddMultisig(t, payload, seed, key, exodus, relay)
	return t.Hex(), nil
}

func createRawTxInput(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(3, 3, "omni_createrawtx_input \"rawtx\" \"txid\" n"); err != nil {
		return nil, err
	}
	t, err := a.rawTx(0)
	if err != nil {
		return nil, err
	}
	txid, err := a.txid(1)
	if err != nil {
		return nil, err
	}
	n, err := a.outputIndex(2)
	if err != nil {
		return nil, err
	}
	wallet.AddInput(t, txid, n)
	return t.Hex(), nil
}

func (s *Server) destination(ctx context.Context, a args, i int) (omni.Destination, error) {
	var addr string
	err := s.read(ctx, func(q *query) error {
		var err error
		addr, err = q.address(a, i)
		return err
	})
	if err != nil {
		return omni.Destination{}, err
	}
	return omni.DecodeAddress(addr, s.Params)
}

func createRawTxReference(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(2, 3, "omni_createrawtx_reference \"rawtx\" \"destination\" ( \"referenceamount\" )"); err != nil {
		return nil, err
	}
	t, err := a.rawTx(0)
	if err != nil {
		return nil, err
	}
	dest, err := s.destination(ctx, a, 1)
	if err != nil {
		return nil, err
	}
	var amount int64
	if len(a) > 2 {
		if amount, err = a.amount(2, true); err != nil {
			return nil, err
		}
	}
	relay, err := s.relayFee(ctx)
	if err != nil {
		return nil, err
	}
	wallet.AddReference(t, dest, amount, relay)
	return t.Hex(), nil
}

func createRawTxChange(ctx context.Context, s *Server, a args) (any, error) {
	if err := a.count(4, 5, "omni_createrawtx_change \"rawtx\" [{\"txid\":\"hash\",\"vout\":n,\"scriptPubKey\":\"hex\",\"value\":n.nnnnnnnn},...] \"destination\" fee ( position )"); err != nil {
		return nil, err
	}
	t, err := a.rawTx(0)
	if err != nil {
		return nil, err
	}
	prevs, err := parsePrevTxs(a[1])
	if err != nil {
		return nil, err
	}
	dest, err := s.destination(ctx, a, 2)
	if err != nil {
		return nil, err
	}
	fee, err := a.coinAmount(3)
	if err != nil {
		return nil, err
	}
	position := int64(0)
	if len(a) > 4 {
		if position, err = a.int64(4); err != nil {
			return nil, err
		}
	}
	relay, err := s.relayFee(ctx)
	if err != nil {
		return nil, err
	}
	wallet.AddChange(t, prevs, dest, fee, int(uint32(position)), relay)
	return t.Hex(), nil
}

// parsePrevTxs mirrors ParsePrevTxs.
func parsePrevTxs(raw json.RawMessage) ([]wallet.Prevout, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errorf(codeType, "Expected type array")
	}
	const shape = "expected object with {\"txid\",\"vout\",\"scriptPubKey\",\"value\":n.nnnnnnnn}"
	res := make([]wallet.Prevout, 0, len(list))
	for _, item := range list {
		var o map[string]json.RawMessage
		if err := json.Unmarshal(item, &o); err != nil || o == nil {
			return nil, errorf(codeDeserialization, shape)
		}
		field := args{o["txid"], o["vout"], o["scriptPubKey"]}
		txid, err := field.txid(0)
		if err != nil {
			return nil, err
		}
		vout, err := field.outputIndex(1)
		if err != nil {
			return nil, err
		}
		script, err := field.str(2)
		if err != nil {
			return nil, err
		}
		b, err := hex.DecodeString(script)
		if err != nil || strings.TrimSpace(script) != script {
			return nil, errorf(codeInvalidParam, "scriptPubKey must be hexadecimal string (not '"+script+"')")
		}
		value, err := amountFromJSON(o["value"])
		if err != nil {
			return nil, err
		}
		res = append(res, wallet.Prevout{Txid: txid, Vout: vout, Script: b, Value: value})
	}
	return res, nil
}
