package omni

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestPayloadBuilders(t *testing.T) {
	cases := []struct {
		got  []byte
		want string
	}{
		{PayloadSimpleSend(1, 100000000), "00000000000000010000000005f5e100"},
		{PayloadSendAll(2), "0000000402"},
		{PayloadSendNonFungible(5, 1, 10), "00000005000000050000000000000001000000000000000a"},
		{PayloadAnyData([]byte{0xbe, 0xef}), "000000c8beef"},
	}
	for _, c := range cases {
		if h := hex.EncodeToString(c.got); h != c.want {
			t.Errorf("payload %s, want %s", h, c.want)
		}
	}
}

func TestFullyValidPubKey(t *testing.T) {
	g, _ := hex.DecodeString("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	if !FullyValidPubKey(g) {
		t.Fatal("generator rejected")
	}
	gu, _ := hex.DecodeString("0479be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8")
	if !FullyValidPubKey(gu) {
		t.Fatal("uncompressed generator rejected")
	}
	gu[64] ^= 1
	if FullyValidPubKey(gu) {
		t.Fatal("point off the curve accepted")
	}
	// x = 5 has no y on secp256k1 (125 + 7 = 132 is not a square mod p).
	k := make([]byte, 33)
	k[0], k[32] = 2, 5
	if FullyValidPubKey(k) {
		t.Fatal("x=5 accepted")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	d, _ := NewDecoder(mainnet)
	sender := Destination{Kind: DestScriptHash, Hash: [20]byte{1, 2, 3}}
	senderAddr := sender.Encode(mainnet)
	ref := Destination{Kind: DestKeyHash, Hash: [20]byte{9}}
	in := []Output{{Script: ScriptForDestination(sender), Value: 100000}}

	small := PayloadSimpleSend(3, 7)
	if !UseClassC(len(small)) || UseClassC(77) || !UseClassC(76) {
		t.Fatal("class C limit")
	}
	opret, err := ClassCScript(small)
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Parse(&Tx{Txid: "c", Inputs: in, Outputs: []Output{
		{Script: ScriptForDestination(sender), Value: 5000}, {Script: opret}, {Script: ScriptForDestination(ref), Value: 5460}}})
	if err != nil || p.Class != ClassC || p.Sender != senderAddr || p.Reference != ref.Encode(mainnet) || !bytes.Equal(p.Payload, small) {
		t.Fatalf("class C: %+v %v", p, err)
	}

	big := PayloadAnyData(bytes.Repeat([]byte{7}, 80))
	key, _ := hex.DecodeString("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	scripts, err := ClassBScripts(senderAddr, key, big)
	if err != nil {
		t.Fatal(err)
	}
	exodus, _ := DecodeAddress(mainnet.Exodus, mainnet)
	outs := []Output{{Script: ScriptForDestination(sender), Value: 5000}}
	for _, s := range scripts {
		outs = append(outs, Output{Script: s, Value: 7860})
	}
	outs = append(outs, Output{Script: ScriptForDestination(exodus), Value: 5460})
	p, err = d.Parse(&Tx{Txid: "b", Inputs: in, Outputs: outs})
	if err != nil || p.Class != ClassB || p.Sender != senderAddr || !bytes.HasPrefix(p.Payload, big) {
		t.Fatalf("class B: %+v %v", p, err)
	}
}

func TestManagementPayloadsRoundTrip(t *testing.T) {
	sender := Destination{Kind: DestScriptHash, Hash: [20]byte{1}}.Encode(mainnet)
	target := Destination{Kind: DestScriptHash, Hash: [20]byte{7}}
	info := &PropertyInfo{Ecosystem: 1, Type: 2, Category: "Games", Name: "Coin", URL: "https://liteworlds.quest", Data: "x"}
	check := func(name string, p []byte, want func(m *Message) bool) {
		t.Helper()
		m, err := Interpret(p, sender, "", mainnet)
		if err != nil || !want(m) {
			t.Errorf("%s: %+v %v", name, m, err)
		}
	}
	check("fixed", PayloadIssuanceFixed(info, 5), func(m *Message) bool {
		return m.Type == TypeCreatePropertyFixed && m.Ecosystem == 1 && m.PropType == 2 && m.Name == "Coin" && m.URL == info.URL && m.Data == "x" && m.Amount == 5
	})
	check("crowdsale", PayloadIssuanceCrowdsale(info, 1, 100, 1800000000, 10, 5), func(m *Message) bool {
		return m.Type == TypeCreatePropertyVar && m.Property == 1 && m.Amount == 100 && m.Deadline == 1800000000 && m.EarlyBird == 10 && m.Percentage == 5
	})
	managed := *info
	managed.Type = 5
	check("managed", PayloadIssuanceManaged(&managed), func(m *Message) bool {
		return m.Type == TypeCreatePropertyMan && m.PropType == 5 && m.Name == "Coin"
	})
	check("sto v0", PayloadSendToOwners(3, 9, 3), func(m *Message) bool {
		return m.Type == TypeSendToOwners && m.Version == 0 && m.Property == 3 && m.Amount == 9
	})
	check("sto v1", PayloadSendToOwners(3, 9, 4), func(m *Message) bool {
		return m.Version == 1 && m.DistributionProperty == 4
	})
	check("grant", PayloadGrant(3, 9, "memo"), func(m *Message) bool {
		return m.Type == TypeGrantTokens && m.Amount == 9 && m.NFTData == "memo"
	})
	check("revoke", PayloadRevoke(3, 9, ""), func(m *Message) bool { return m.Type == TypeRevokeTokens && m.Amount == 9 })
	check("close", PayloadCloseCrowdsale(3), func(m *Message) bool { return m.Type == TypeCloseCrowdsale && m.Property == 3 })
	check("issuer", PayloadChangeIssuer(3), func(m *Message) bool { return m.Type == TypeChangeIssuer && m.Property == 3 })
	check("enable", PayloadEnableFreezing(3), func(m *Message) bool { return m.Type == TypeEnableFreezing })
	check("disable", PayloadDisableFreezing(3), func(m *Message) bool { return m.Type == TypeDisableFreezing })
	// An M... target must survive HashToAddress, which only knows version 5.
	check("freeze", PayloadFreeze(true, 3, 1, target, mainnet), func(m *Message) bool {
		return m.Type == TypeFreezeTokens && m.Receiver == target.Encode(mainnet)
	})
	check("unfreeze", PayloadFreeze(false, 3, 1, Destination{Kind: DestKeyHash, Hash: [20]byte{8}}, mainnet), func(m *Message) bool {
		return m.Type == TypeUnfreezeTokens && m.Receiver == (Destination{Kind: DestKeyHash, Hash: [20]byte{8}}).Encode(mainnet)
	})
	check("nft data", PayloadSetNonFungibleData(3, 1, 10, true, "hello"), func(m *Message) bool {
		return m.Type == TypeNonFungibleData && m.TokenStart == 1 && m.TokenEnd == 10 && m.NFTDataType == 1 && m.NFTData == "hello"
	})
	// Texts are cut to 255 bytes like in OmniLite.
	long := *info
	long.Name = string(bytes.Repeat([]byte{'a'}, 300))
	if p := PayloadIssuanceManaged(&long); len(p) != 4+1+2+4+len("Games")+1+1+255+1+len(info.URL)+1+2 {
		t.Errorf("long name payload has %d bytes", len(p))
	}
}
