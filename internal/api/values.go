package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"github.com/tbuuol/omnilite-standalone/internal/mempool"
	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/state"
	"github.com/tbuuol/omnilite-standalone/internal/store"
)

// object is a JSON object that keeps its key order, as UniValue does.
type object struct {
	keys []string
	vals []any
}

func (o *object) add(k string, v any) *object {
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
	return o
}

func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// array is a JSON array that encodes as [] when empty.
type array []any

func (a array) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]any(a))
}

// formatDivisible mirrors FormatDivisibleMP without sign: the absolute
// value with eight decimals.
func formatDivisible(n int64) string {
	if n == math.MinInt64 {
		return "ErrorAmount"
	}
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("%d.%08d", n/100000000, n%100000000)
}

func formatIndivisible(n int64) string { return strconv.FormatInt(n, 10) }

// formatByType mirrors FormatByType: property types with bit 1 set are indivisible.
func formatByType(n int64, propType uint16) string {
	if propType&1 != 0 {
		return formatIndivisible(n)
	}
	return formatDivisible(n)
}

// query is one API call's view of the state.
// Pool is the mempool view the API reads unconfirmed transactions from.
type Pool interface {
	Get(txid string) *mempool.Entry
	List() []*mempool.Entry
	PendingDebit(addr string, property uint32) int64
}

type query struct {
	snap   *store.Snapshot
	st     *state.State
	params *chain.Params
	pool   Pool
}

func newQuery(snap *store.Snapshot, p *chain.Params, pool Pool) *query {
	return &query{snap: snap, st: state.New(snap, p, state.Options{}), params: p, pool: pool}
}

// formatMP mirrors FormatMP.
func (q *query) formatMP(id uint32, n int64) (string, error) {
	div, err := q.st.Divisible(id)
	if err != nil {
		return "", err
	}
	if div {
		return formatDivisible(n), nil
	}
	return formatIndivisible(n), nil
}

// height is the current block (GetHeight): the indexer's tip.
func (q *query) height() (int64, error) {
	tip, err := q.snap.Tip()
	if err != nil || tip == nil {
		return 0, err
	}
	return tip.Height, nil
}

// args are the positional parameters of a call.
type args []json.RawMessage

func (a args) has(i int) bool { return i < len(a) && string(a[i]) != "null" }

func (a args) str(i int) (string, error) {
	if !a.has(i) {
		return "", errorf(codeMisc, "JSON value is not a string as expected")
	}
	var s string
	if err := json.Unmarshal(a[i], &s); err != nil {
		return "", errorf(codeMisc, "JSON value is not a string as expected")
	}
	return s, nil
}

func (a args) int64(i int) (int64, error) {
	if !a.has(i) {
		return 0, errorf(codeMisc, "JSON value is not an integer as expected")
	}
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(a[i]))
	d.UseNumber()
	if err := d.Decode(&n); err != nil {
		return 0, errorf(codeMisc, "JSON value is not an integer as expected")
	}
	v, err := n.Int64()
	if err != nil {
		return 0, errorf(codeMisc, "JSON integer out of range")
	}
	return v, nil
}

func (a args) boolean(i int) (bool, error) {
	var b bool
	if err := json.Unmarshal(a[i], &b); err != nil {
		return false, errorf(codeMisc, "JSON value is not a boolean as expected")
	}
	return b, nil
}

// count checks the number of parameters like the request.params.size()
// checks, which print the help text in OmniLite.
func (a args) count(min, max int, usage string) error {
	if len(a) < min || len(a) > max {
		return errorf(codeMisc, usage)
	}
	return nil
}

// address mirrors ParseAddress: any valid address, re-encoded.
func (q *query) address(a args, i int) (string, error) {
	s, err := a.str(i)
	if err != nil {
		return "", err
	}
	d, err := omni.DecodeAddress(s, q.params)
	if err != nil {
		return "", errorf(codeInvalidAddress, "Invalid address")
	}
	return d.Encode(q.params), nil
}

// propertyID mirrors ParsePropertyId.
func (a args) propertyID(i int) (uint32, error) {
	n, err := a.int64(i)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > math.MaxUint32 {
		return 0, errorf(codeInvalidParam, "Property identifier is out of range")
	}
	return uint32(n), nil
}

// txid mirrors ParseHashV.
func (a args) txid(i int) (string, error) {
	s, err := a.str(i)
	if err != nil {
		return "", err
	}
	if len(s) != 64 || strings.Trim(strings.ToLower(s), "0123456789abcdef") != "" {
		return "", errorf(codeInvalidParam, "txid must be of length 64 (not "+strconv.Itoa(len(s))+", for '"+s+"')")
	}
	return strings.ToLower(s), nil
}

// requireExisting mirrors RequireExistingProperty.
func (q *query) requireExisting(id uint32) (*state.Property, error) {
	ok, err := q.st.ValidPropertyID(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errorf(codeInvalidParam, "Property identifier does not exist")
	}
	p, err := q.st.Property(id)
	if err == nil && p == nil {
		err = errorf(codeDatabase, "Failed to retrieve property")
	}
	return p, err
}

// strToInt64 mirrors StrToInt64 for amount strings.
func strToInt64(s string, divisible bool) int64 {
	if strings.Contains(s, "-") {
		return 0
	}
	if !divisible {
		if i := strings.IndexByte(s, '.'); i >= 0 {
			s = s[:i]
		}
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	if i := strings.IndexByte(s, '.'); i < 0 {
		s += "00000000"
	} else {
		if strings.IndexByte(s[i+1:], '.') >= 0 {
			return 0
		}
		if len(s)-i < 9 {
			s += strings.Repeat("0", 8-(len(s)-i-1))
		} else {
			s = s[:i+9]
		}
		s = strings.Replace(s, ".", "", 1)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
