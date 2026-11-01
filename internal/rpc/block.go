package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Tx is a decoded transaction as returned by getblock (verbosity 2) and
// getrawtransaction (verbose).
type Tx struct {
	Txid string  `json:"txid"`
	Vin  []TxIn  `json:"vin"`
	Vout []TxOut `json:"vout"`
}

type TxIn struct {
	Txid     string `json:"txid"`
	Vout     uint32 `json:"vout"`
	Coinbase string `json:"coinbase"`
}

type TxOut struct {
	Value        json.Number `json:"value"`
	N            uint32      `json:"n"`
	ScriptPubKey struct {
		Hex string `json:"hex"`
	} `json:"scriptPubKey"`
}

// IsCoinbase reports whether the transaction is a coinbase.
func (t *Tx) IsCoinbase() bool { return len(t.Vin) == 1 && t.Vin[0].Coinbase != "" }

type Block struct {
	Hash   string `json:"hash"`
	Height int64  `json:"height"`
	Time   int64  `json:"time"`
	Tx     []Tx   `json:"tx"`
}

// GetBlock returns a block with all transactions decoded.
func (c *Client) GetBlock(ctx context.Context, hash string) (*Block, error) {
	var r Block
	return &r, c.Call(ctx, "getblock", &r, hash, 2)
}

// GetRawTransaction returns a decoded transaction. Transactions in blocks
// are only found with -txindex.
func (c *Client) GetRawTransaction(ctx context.Context, txid string) (*Tx, error) {
	var r Tx
	return &r, c.Call(ctx, "getrawtransaction", &r, txid, true)
}

// Litoshi converts an amount as printed by litecoind ("0.00005460") to
// litoshi without going through floating point.
func Litoshi(v json.Number) (int64, error) {
	s := string(v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > 8 || strings.ContainsAny(s, "eE+") {
		return 0, fmt.Errorf("unexpected amount %q", v)
	}
	frac += strings.Repeat("0", 8-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected amount %q", v)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected amount %q", v)
	}
	n := w*100_000_000 + f
	if neg {
		n = -n
	}
	return n, nil
}
