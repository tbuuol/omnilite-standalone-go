package api

import (
	"strings"
	"testing"
)

func TestCreatePayloads(t *testing.T) {
	f := standard(t)
	f.want("omni_createpayload_simplesend", `"00000000000000030000000008f0d180"`, 3, "1.5")
	f.want("omni_createpayload_simplesend", `{"code":-3,"message":"Invalid amount"}`, 3, "0")
	f.want("omni_createpayload_sendall", `"0000000401"`, 1)
	f.want("omni_createpayload_sendall", `{"code":-8,"message":"Invalid ecosystem (1 = main, 2 = test only)"}`, 3)
	f.want("omni_createpayload_sendnonfungible", `"00000005000000070000000000000001000000000000000a"`, 7, 1, 10)
	f.want("omni_createpayload_sendnonfungible", `{"code":-8,"message":"Unique range start value must be higher than non-fungible range end value"}`, 7, 10, 1)
	f.want("omni_createpayload_anydata", `"000000c8beef"`, "beef")
	f.want("omni_createpayload_anydata", `{"code":-8,"message":"data must be hexadecimal string (not 'xyz')"}`, "xyz")
}

func TestCreateRawTx(t *testing.T) {
	f := standard(t)
	payload := "00000000000000030000000000000001"
	opret := "020000000001" + "0000000000000000" + "16" + "6a14" + "6f6d6e69" + payload + "00000000"
	f.want("omni_createrawtx_opreturn", `"`+opret+`"`, "", payload)

	withRef := "020000000002" + "0000000000000000" + "16" + "6a14" + "6f6d6e69" + payload +
		"1815000000000000" + "17a914" + strings.Repeat("b2", 20) + "87" + "00000000"
	f.want("omni_createrawtx_reference", `"`+withRef+`"`, opret, bob)

	input := "0200000001" + "01" + strings.Repeat("00", 31) + "00000000" + "00" + "ffffffff" + withRef[10:]
	f.want("omni_createrawtx_input", `"`+input+`"`, withRef, txid(1), 0)

	prev := []map[string]any{{"txid": txid(1), "vout": 0, "scriptPubKey": "a914" + strings.Repeat("a1", 20) + "87", "value": 0.001}}
	change := input[:len("0200000001")+82] + "03" + "784a010000000000" + "17a914" + strings.Repeat("a1", 20) + "87" + input[len("0200000001")+84:]
	f.want("omni_createrawtx_change", `"`+change+`"`, input, prev, alice, 0.0001)
	// A missing prevout leaves the transaction unchanged.
	f.want("omni_createrawtx_change", `"`+input+`"`, input, []any{}, alice, 0.0001)

	f.want("omni_createrawtx_opreturn", `{"code":-22,"message":"Transaction deserialization failed"}`, "zz", payload)
	f.want("omni_createrawtx_input", `{"code":-22,"message":"Vout index must be positive"}`, "", txid(1), -1)
}

func TestSendDisabled(t *testing.T) {
	f := standard(t)
	f.want("omni_send", `{"code":-32601,"message":"Method not found (sending is disabled, set wallet=1)"}`, alice, bob, 3, "1")
}
