package api

import (
	"strings"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/wallet"
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

func TestManagementPayloads(t *testing.T) {
	f := standard(t)
	f.want("omni_createpayload_issuancemanaged", `"0000003601000500000000476d6500004e4654000000"`, 1, 5, 0, "Gme", "", "NFT", "", "")
	f.want("omni_createpayload_issuancemanaged", `{"code":-8,"message":"Invalid property type (1 = indivisible, 2 = divisible, 5 = non-fungible only)"}`, 1, 3, 0, "", "", "NFT", "", "")
	f.want("omni_createpayload_issuancefixed", `{"code":-3,"message":"Property appends/replaces are not yet supported"}`, 1, 1, 7, "", "", "X", "", "", "1")
	f.want("omni_createpayload_issuancefixed", `{"code":-3,"message":"Property name must not be empty"}`, 1, 1, 0, "", "", "", "", "", "1")
	f.want("omni_createpayload_issuancecrowdsale", `{"code":-8,"message":"Properties must be in the same ecosystem"}`, 1, 2, 0, "", "", "X", "", "", 2, "1", 0, 0, 0)
	f.want("omni_createpayload_sto", `"0001000300000003000000000bebc20000000004"`, 3, "2", 4)
	f.want("omni_createpayload_grant", `{"code":-1,"message":"omni_createpayload_grant propertyid \"amount\" ( \"memo\" )"}`, 3, "1")
	f.want("omni_createpayload_closecrowdsale", `"0000003500000003"`, 3)
	// P2SH targets get version byte 5, so OmniLite can read them back.
	f.want("omni_createpayload_freeze", `"000000b9000000030000000005f5e10005`+strings.Repeat("b2", 20)+`"`, bob, 3, "1")
	f.want("omni_createpayload_setnonfungibledata", `"000000c9000000030000000000000001000000000000000201616200"`, 3, 1, 2, true, "ab")
}

func TestManagementRequirements(t *testing.T) {
	f := standard(t)
	f.server.Wallet = &wallet.Wallet{} // checks fail before anything is built
	f.want("omni_sendgrant", `{"code":-8,"message":"Property identifier does not refer to a managed property"}`, alice, bob, 3, "1")
	f.want("omni_sendchangeissuer", `{"code":-3,"message":"Sender is not authorized to manage the property"}`, bob, alice, 3)
	f.want("omni_sendclosecrowdsale", `{"code":-8,"message":"Property identifier does not refer to a crowdsale"}`, alice, 3)
	f.want("omni_sendsto", `{"code":-3,"message":"Sender has insufficient balance"}`, alice, 3, "5000")
	f.want("omni_setnonfungibledata", `{"code":-8,"message":"Property identifier does not refer to a non-fungible property"}`, 3, 1, 1, true, "x")
	f.want("omni_sendissuancemanaged", `{"code":-3,"message":"Property name must not be empty"}`, alice, 1, 1, 0, "", "", "", "", "")
}

func TestDExRequirements(t *testing.T) {
	f := standard(t)
	f.want("omni_createpayload_dexaccept", `"00000016000000030000000005f5e100"`, 3, "1")
	f.server.Wallet = &wallet.Wallet{}
	f.want("omni_sendnewdexorder", `{"code":-3,"message":"Another active sell offer from the given address already exists on the distributed exchange"}`, alice, 3, "1", "0.1", 10, "0.0001")
	f.want("omni_sendupdatedexorder", `{"code":-3,"message":"Payment window must be within 1-255 blocks"}`, alice, 3, "1", "0.1", 0, "0.0001")
	f.want("omni_sendcanceldexorder", `{"code":-3,"message":"No matching sell offer on the distributed exchange"}`, bob, 3)
	f.want("omni_senddexsell", `{"code":-3,"message":"Invalid action (1 = new, 2 = update, 3 = cancel only)"}`, alice, 3, "1", "0.1", 10, "0.0001", 4)
	f.want("omni_senddexaccept", `{"code":-3,"message":"No matching sell offer on the distributed exchange"}`, alice, bob, 3, "1")
	f.want("omni_senddexpay", `{"code":-3,"message":"No matching accept order on the distributed exchange"}`, bob, alice, 3, "0.1")
	f.want("omni_senddexpay", `{"code":-5,"message":"Invalid buyer address"}`, "ltc1qxyz", alice, 3, "0.1")
}

func TestFormatMoney(t *testing.T) {
	for n, want := range map[int64]string{0: "0.00", 50000000: "0.50", 123456789: "1.23456789", 100000000: "1.00", 1000: "0.00001"} {
		if got := formatMoney(n); got != want {
			t.Errorf("formatMoney(%d) = %s, want %s", n, got, want)
		}
	}
}
