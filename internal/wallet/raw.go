package wallet

import (
	"context"
	"encoding/json"

	"github.com/tbuuol/omnilite-standalone/internal/omni"
	"github.com/tbuuol/omnilite-standalone/internal/rpc"
)

// Building blocks for omni_createrawtx_* (createtx.cpp). They extend a
// transaction step by step and leave it unchanged when a step cannot be
// done, as OmniTxBuilder does.

// RelayFee returns the node's minimum relay fee in litoshi per kB, which
// the dust thresholds are based on.
func RelayFee(ctx context.Context, node Node) (int64, error) {
	var ni struct {
		RelayFee json.Number `json:"relayfee"`
	}
	if err := node.Call(ctx, "getnetworkinfo", &ni); err != nil {
		return 0, err
	}
	return rpc.Litoshi(ni.RelayFee)
}

// AddOpReturn appends the Class C output.
func AddOpReturn(t *Tx, payload []byte) {
	if s, err := omni.ClassCScript(payload); err == nil {
		t.Outputs = append(t.Outputs, TxOut{Script: s})
	}
}

// AddMultisig appends the Class B outputs and the Exodus output.
func AddMultisig(t *Tx, payload []byte, seed string, key []byte, exodus omni.Destination, relayFee int64) {
	scripts, err := omni.ClassBScripts(seed, key, payload)
	if err != nil {
		return
	}
	for _, s := range append(scripts, omni.ScriptForDestination(exodus)) {
		t.Outputs = append(t.Outputs, TxOut{Value: omniDust(s, relayFee), Script: s})
	}
}

// AddInput appends an input spending txid:vout.
func AddInput(t *Tx, txid string, vout uint32) {
	t.Inputs = append(t.Inputs, TxIn{Txid: txid, Vout: vout, Sequence: 0xffffffff})
}

// AddReference appends an output to dest worth at least the dust threshold.
func AddReference(t *Tx, dest omni.Destination, value, relayFee int64) {
	s := omni.ScriptForDestination(dest)
	t.Outputs = append(t.Outputs, TxOut{Value: max(omniDust(s, relayFee), value), Script: s})
}

// Prevout is an output spent by the transaction, as given in prevtxs.
type Prevout struct {
	Txid   string
	Vout   uint32
	Script []byte
	Value  int64
}

// AddChange inserts the change to dest at position (or at the end). It
// does nothing if an input is not in prevs or the change would be dust.
func AddChange(t *Tx, prevs []Prevout, dest omni.Destination, fee int64, position int, relayFee int64) {
	var in int64
	for _, txin := range t.Inputs {
		found := false
		for _, p := range prevs {
			if p.Txid == txin.Txid && p.Vout == txin.Vout {
				in += p.Value
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
	var out int64
	for _, o := range t.Outputs {
		out += o.Value
	}
	s := omni.ScriptForDestination(dest)
	change := in - out - fee
	if change < omniDust(s, relayFee) {
		return
	}
	if position < 0 || position > len(t.Outputs) {
		position = len(t.Outputs)
	}
	t.Outputs = append(t.Outputs[:position], append([]TxOut{{Value: change, Script: s}}, t.Outputs[position:]...)...)
}
