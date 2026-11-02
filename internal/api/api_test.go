package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/indexer"
	"github.com/tbuuol/omnilite-standalone/internal/mempool"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

var params = &chain.Main

func addr(b byte) string {
	d := omni.Destination{Kind: omni.DestScriptHash}
	for i := range d.Hash {
		d.Hash[i] = b
	}
	return d.Encode(params)
}

var (
	alice = addr(0xa1)
	bob   = addr(0xb2)
)

func txid(n int) string { return fmt.Sprintf("%064x", n) }

func fixedPayload(name string, amount uint64) []byte {
	p := []byte{0, 0, 0, 50, 1, 0, 2, 0, 0, 0, 0}
	for _, s := range []string{"Games", "", name, "https://liteworlds.quest", ""} {
		p = append(append(p, s...), 0)
	}
	return binary.BigEndian.AppendUint64(p, amount)
}

func sendPayload(id uint32, amount uint64) []byte {
	p := []byte{0, 0, 0, 0}
	p = binary.BigEndian.AppendUint32(p, id)
	return binary.BigEndian.AppendUint64(p, amount)
}

func offerPayload(id uint32, amount, desired uint64) []byte {
	p := []byte{0, 1, 0, 20}
	p = binary.BigEndian.AppendUint32(p, id)
	p = binary.BigEndian.AppendUint64(p, amount)
	p = binary.BigEndian.AppendUint64(p, desired)
	p = append(p, 10)
	p = binary.BigEndian.AppendUint64(p, 10000)
	return append(p, 1)
}

type fakeNode struct{}

func (fakeNode) Call(_ context.Context, method string, result any, params ...any) error {
	raw, ok := result.(*json.RawMessage)
	if !ok {
		// A call made by the server itself.
		if method == "getnetworkinfo" {
			return json.Unmarshal([]byte(`{"relayfee":0.00001000}`), result)
		}
		return fmt.Errorf("unexpected call %s", method)
	}
	*raw = json.RawMessage(fmt.Sprintf(`{"method":%q,"params":%d}`, method, len(params)))
	return nil
}

type fixture struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

type tx struct {
	sender, ref string
	payload     []byte
}

func newFixture(t *testing.T, blocks ...[]tx) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), "main", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	n, prev := 0, ""
	for i, txs := range blocks {
		height := params.OmniGenesis + int64(i)
		hash := fmt.Sprintf("%064x", 0xb10c0000+i)
		btx, err := st.Begin(ctx, store.Block{Height: height, Hash: hash, PrevHash: prev, Time: 1700000000 + int64(i)*600})
		if err != nil {
			t.Fatal(err)
		}
		s := state.New(btx, params, state.Options{})
		if err := s.BeginBlock(height, 1700000000+int64(i)*600, hash); err != nil {
			t.Fatal(err)
		}
		for idx, x := range txs {
			n++
			m, err := omni.Interpret(x.payload, x.sender, x.ref, params)
			if err != nil {
				t.Fatal(err)
			}
			rec := indexer.Record{Kind: indexer.KindOmni, Txid: txid(n), Block: height, Index: idx + 1, Time: 1700000000 + int64(i)*600,
				Class: "C", Sender: x.sender, Reference: x.ref, Fee: 2000, Version: m.Version, Type: m.Type, Payload: hex.EncodeToString(x.payload)}
			if _, err := s.Apply(&state.Tx{Txid: rec.Txid, Block: height, Index: rec.Index, BlockHash: hash, BlockTime: rec.Time, Sender: x.sender, Fee: rec.Fee, Msg: m}); err != nil {
				t.Fatal(err)
			}
			v, _ := json.Marshal(rec)
			btx.Put(indexer.NsTx, rec.Txid, v)
			btx.Put(indexer.NsBlockTx, indexer.BlockTxKey(height, rec.Index), []byte(rec.Txid))
			btx.Put(indexer.NsCount, "total", []byte(fmt.Sprint(n)))
		}
		if err := s.EndBlock(height, 0); err != nil {
			t.Fatal(err)
		}
		if err := btx.Commit(); err != nil {
			t.Fatal(err)
		}
		prev = hash
	}
	srv := httptest.NewServer(&Server{Store: st, Params: params, Node: fakeNode{}, User: "u", Pass: "p"})
	t.Cleanup(srv.Close)
	return &fixture{t: t, st: st, srv: srv}
}

// call returns the HTTP status and the raw result or error JSON.
func (f *fixture) call(method string, params ...any) (int, string) {
	f.t.Helper()
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL, bytes.NewReader(body))
	req.SetBasicAuth("u", "p")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	data, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(data, &r); err != nil {
		f.t.Fatalf("%s: %s", method, data)
	}
	if string(r.Error) != "null" {
		return resp.StatusCode, string(r.Error)
	}
	return resp.StatusCode, string(r.Result)
}

func (f *fixture) want(method, want string, params ...any) {
	f.t.Helper()
	if _, got := f.call(method, params...); got != want {
		f.t.Errorf("%s %v\n got: %s\nwant: %s", method, params, got, want)
	}
}

func standard(t *testing.T) *fixture {
	return newFixture(t,
		[]tx{{alice, "", fixedPayload("Coin", 1000_00000000)}},
		[]tx{{alice, bob, sendPayload(3, 300_00000000)}, {alice, bob, sendPayload(3, 5000_00000000)}},
		[]tx{{alice, "", offerPayload(3, 100_00000000, 50000000)}},
	)
}

func TestProperty(t *testing.T) {
	f := standard(t)
	f.want("omni_getproperty", `{"propertyid":3,"name":"Coin","category":"Games","subcategory":"","data":"","url":"https://liteworlds.quest","divisible":true,"issuer":"`+alice+`","creationtxid":"`+txid(1)+`","fixedissuance":true,"managedissuance":false,"non-fungibletoken":false,"totaltokens":"1000.00000000"}`, 3)
	f.want("omni_getproperty", `{"code":-8,"message":"Property identifier does not exist"}`, 4)
	f.want("omni_getproperty", `{"code":-8,"message":"Property identifier is out of range"}`, 0)
	_, list := f.call("omni_listproperties")
	if !strings.HasPrefix(list, `[{"propertyid":1,"name":"Reserved"`) || !strings.Contains(list, `{"propertyid":3,"name":"Coin"`) {
		t.Errorf("listproperties = %s", list)
	}
}

func TestBalances(t *testing.T) {
	f := standard(t)
	f.want("omni_getallbalancesforaddress", `[{"propertyid":3,"name":"Coin","balance":"600.00000000","reserved":"100.00000000","frozen":"0.00000000"}]`, alice)
	f.want("omni_getbalance", `{"balance":"300.00000000","reserved":"0.00000000","frozen":"0.00000000"}`, bob, 3)
	f.want("omni_getallbalancesforaddress", `{"code":-8,"message":"Address not found"}`, addr(0x01))
	f.want("omni_getallbalancesforaddress", `{"code":-5,"message":"Invalid address"}`, "nope")
	_, all := f.call("omni_getallbalancesforid", 3)
	if strings.Count(all, `"address"`) != 2 {
		t.Errorf("getallbalancesforid = %s", all)
	}
}

func TestTransaction(t *testing.T) {
	f := standard(t)
	common := `"fee":"0.00002000","sendingaddress":"` + alice + `","referenceaddress":"` + bob + `","ismine":false,"version":0,"type_int":0,"type":"Simple Send","propertyid":3,"divisible":true,`
	f.want("omni_gettransaction", `{"txid":"`+txid(2)+`",`+common+`"amount":"300.00000000","valid":true,"blockhash":"`+fmt.Sprintf("%064x", 0xb10c0001)+`","blocktime":1700000600,"positioninblock":1,"block":2010501,"confirmations":2}`, txid(2))
	f.want("omni_gettransaction", `{"txid":"`+txid(3)+`",`+common+`"amount":"5000.00000000","valid":false,"invalidreason":"Sender has insufficient balance","blockhash":"`+fmt.Sprintf("%064x", 0xb10c0001)+`","blocktime":1700000600,"positioninblock":2,"block":2010501,"confirmations":2}`, txid(3))
	f.want("omni_gettransaction", `{"code":-5,"message":"No Omni Layer Protocol transaction"}`, txid(99))
	f.want("omni_listblocktransactions", `["`+txid(2)+`","`+txid(3)+`"]`, 2010501)
	f.want("omni_listblocktransactions", `{"code":-8,"message":"Block height is out of range"}`, 2010503)

	_, fixed := f.call("omni_gettransaction", txid(1))
	if !strings.Contains(fixed, `"type":"Create Property - Fixed","propertyid":3,"divisible":true,"ecosystem":"main","propertytype":"divisible","category":"Games"`) {
		t.Errorf("fixed issuance = %s", fixed)
	}
	_, offer := f.call("omni_gettransaction", txid(4))
	if !strings.Contains(offer, `"amount":"100.00000000","litecoindesired":"0.50000000","timelimit":10,"feerequired":"0.00010000","action":"new","valid":true`) {
		t.Errorf("offer = %s", offer)
	}
}

func TestDExSells(t *testing.T) {
	f := standard(t)
	f.want("omni_getactivedexsells", `[{"txid":"`+txid(4)+`","propertyid":3,"seller":"`+alice+`","amountavailable":"100.00000000","litecoindesired":"0.50000000","unitprice":"0.00500000","timelimit":10,"minimumfee":"0.00010000","amountaccepted":"0.00000000","accepts":[]}]`)
	f.want("omni_getactivedexsells", `[]`, bob)
}

func TestInfoAndHash(t *testing.T) {
	f := standard(t)
	_, info := f.call("omni_getinfo")
	if !strings.HasPrefix(info, `{"omnicoreversion_int":90001000,"omnicoreversion":"0.9.1","litecoincoreversion":"","block":2010502,"blocktime":1700001200,"blocktransactions":1,"totaltransactions":4,"alerts":[]}`) {
		t.Errorf("getinfo = %s", info)
	}
	_, h := f.call("omni_getcurrentconsensushash")
	if !strings.HasPrefix(h, `{"block":2010502,"blockhash":"`) || !strings.Contains(h, `"consensushash":"`) {
		t.Errorf("consensus hash = %s", h)
	}
}

func TestPayloadDExSell(t *testing.T) {
	f := standard(t)
	f.want("omni_createpayload_dexsell", `"000100140000000300000002540be4000000000002faf0800a000000000000271001"`, 3, "100", "0.5", 10, "0.0001", 1)
	f.want("omni_createpayload_dexsell", `"00010014000000030000000000000000000000000000000000000000000000000003"`, 3, "", "", 0, "", 3)
}

func TestServerProtocol(t *testing.T) {
	f := standard(t)
	status, res := f.call("getrawtransaction", txid(1), true)
	if status != 200 || res != `{"method":"getrawtransaction","params":2}` {
		t.Errorf("pass-through = %d %s", status, res)
	}
	if status, res := f.call("sendtoaddress", alice, 1); status != 404 || !strings.Contains(res, `-32601`) {
		t.Errorf("unknown method = %d %s", status, res)
	}

	req, _ := http.NewRequest(http.MethodPost, f.srv.URL, strings.NewReader(`{"method":"omni_getinfo","params":[]}`))
	req.SetBasicAuth("u", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad password: status %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, f.srv.URL, strings.NewReader(`[{"id":1,"method":"omni_getproperty","params":[3]},{"id":2,"method":"omni_getproperty","params":[9]}]`))
	req.SetBasicAuth("u", "p")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(data), `"name":"Coin"`) || !strings.Contains(string(data), `"Property identifier does not exist"`) {
		t.Errorf("batch = %s", data)
	}
}

type fakePool struct {
	entries []*mempool.Entry
	debit   int64
}

func (p *fakePool) Get(txid string) *mempool.Entry {
	for _, e := range p.entries {
		if e.Record.Txid == txid {
			return e
		}
	}
	return nil
}
func (p *fakePool) List() []*mempool.Entry { return p.entries }
func (p *fakePool) PendingDebit(addr string, id uint32) int64 {
	return map[bool]int64{true: p.debit}[addr == alice && id == 3]
}

func TestPending(t *testing.T) {
	f := standard(t)
	payload := sendPayload(3, 25_00000000)
	pool := &fakePool{debit: 25_00000000, entries: []*mempool.Entry{
		{Record: &indexer.Record{Txid: txid(50), Sender: alice, Reference: bob, Fee: 1000, Payload: hex.EncodeToString(payload)}},
		// Already confirmed, the next poll would drop it.
		{Record: &indexer.Record{Txid: txid(2), Sender: alice, Reference: bob, Payload: hex.EncodeToString(sendPayload(3, 1))}},
	}}
	f.srv.Config.Handler.(*Server).Pool = pool

	want := `{"txid":"` + txid(50) + `","fee":"0.00001000","sendingaddress":"` + alice + `","referenceaddress":"` + bob + `","ismine":false,"version":0,"type_int":0,"type":"Simple Send","propertyid":3,"divisible":true,"amount":"25.00000000","confirmations":0}`
	f.want("omni_gettransaction", want, txid(50))
	f.want("omni_listpendingtransactions", "["+want+"]")
	f.want("omni_listpendingtransactions", "["+want+"]", bob)
	f.want("omni_listpendingtransactions", "[]", addr(0x01))
	// The pending send lowers the available balance until it confirms.
	f.want("omni_getbalance", `{"balance":"575.00000000","reserved":"100.00000000","frozen":"0.00000000"}`, alice, 3)
}
