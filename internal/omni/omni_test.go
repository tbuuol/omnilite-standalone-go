package omni

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

var mainnet = &chain.Main

func push(data []byte) []byte {
	switch {
	case len(data) < opPushData1:
		return append([]byte{byte(len(data))}, data...)
	case len(data) <= 0xff:
		return append([]byte{opPushData1, byte(len(data))}, data...)
	}
	panic("push too long for test")
}

func hash20(b byte) []byte { return bytes.Repeat([]byte{b}, 20) }

func p2pkh(h []byte) []byte {
	return append(append([]byte{opDup, opHash160}, push(h)...), opEqualVerify, opCheckSig)
}

func p2sh(h []byte) []byte { return append(append([]byte{opHash160, 0x14}, h...), opEqual) }

func p2wpkh(h []byte) []byte { return append([]byte{op0, 0x14}, h...) }

func nullData(data ...[]byte) []byte {
	s := []byte{opReturn}
	for _, d := range data {
		s = append(s, push(d)...)
	}
	return s
}

func multisig(m int, keys ...[]byte) []byte {
	s := []byte{byte(op1 + m - 1)}
	for _, k := range keys {
		s = append(s, push(k)...)
	}
	return append(s, byte(op1+len(keys)-1), opCheckMultisig)
}

func addr(kind DestKind, h []byte) string {
	d := Destination{Kind: kind}
	copy(d.Hash[:], h)
	return d.Encode(mainnet)
}

func exodusScript(t *testing.T) []byte {
	t.Helper()
	d, err := DecodeAddress(mainnet.Exodus, mainnet)
	if err != nil {
		t.Fatal(err)
	}
	return p2pkh(d.Hash[:])
}

func simpleSend(property uint32, amount uint64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint32(b[4:], property)
	binary.BigEndian.PutUint64(b[8:], amount)
	return b
}

func decoder(t *testing.T) *Decoder {
	t.Helper()
	d, err := NewDecoder(mainnet)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExodusAddresses(t *testing.T) {
	d, err := DecodeAddress(chain.Main.Exodus, &chain.Main)
	if err != nil {
		t.Fatal(err)
	}
	// The scriptPubKey OmniLite matches in GetEncodingClass.
	if got := hex.EncodeToString(p2pkh(d.Hash[:])); got != "76a9145c0bc5abc545e8c7387934507bcd20ea0fcb2ff288ac" {
		t.Fatalf("exodus script = %s", got)
	}
	if d.Encode(&chain.Main) != chain.Main.Exodus {
		t.Fatal("exodus address does not round-trip")
	}
	for _, p := range []*chain.Params{&chain.Testnet, &chain.Regtest} {
		if _, err := NewDecoder(p); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
	}
}

func TestAddressPrefixes(t *testing.T) {
	if a := addr(DestKeyHash, hash20(1)); a[0] != 'L' {
		t.Fatalf("P2PKH address %s", a)
	}
	if a := addr(DestScriptHash, hash20(1)); a[0] != 'M' {
		t.Fatalf("P2SH address %s", a)
	}
	// The legacy "3..." form decodes to the same destination.
	legacy := base58CheckEncode(mainnet.ScriptPrefix, hash20(1))
	d, err := DecodeAddress(legacy, mainnet)
	if err != nil || d.Encode(mainnet) != addr(DestScriptHash, hash20(1)) {
		t.Fatalf("legacy P2SH %s: %v %v", legacy, d, err)
	}
}

func TestSolve(t *testing.T) {
	key := append([]byte{2}, bytes.Repeat([]byte{7}, 32)...)
	nonMinimal := append(append([]byte{opDup, opHash160, opPushData1, 20}, hash20(3)...), opEqualVerify, opCheckSig)
	tests := []struct {
		name   string
		script []byte
		want   ScriptType
	}{
		{"p2pkh", p2pkh(hash20(1)), PubKeyHash},
		{"p2pkh non-minimal push", nonMinimal, PubKeyHash},
		{"p2sh", p2sh(hash20(1)), ScriptHash},
		{"p2wpkh", p2wpkh(hash20(1)), WitnessV0KeyHash},
		{"taproot", append([]byte{op1, 32}, bytes.Repeat([]byte{1}, 32)...), NonStandard},
		{"p2pk", append(push(key), opCheckSig), PubKey},
		{"multisig 1-of-2", multisig(1, key, key), Multisig},
		{"multisig 3-of-2", multisig(3, key, key), NonStandard},
		{"nulldata", nullData([]byte("omni")), NullData},
		{"bare op_return", []byte{opReturn}, NonStandard},
		{"truncated push", []byte{opReturn, 5, 1}, NonStandard},
	}
	for _, tt := range tests {
		if got, _ := Solve(tt.script); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestClassC(t *testing.T) {
	d := decoder(t)
	a, r, b := hash20(0xa1), hash20(0xb2), hash20(0xc3)
	payload := simpleSend(1, 100)
	ins := []Output{{p2pkh(a), 100_000}, {p2pkh(b), 900_000}}

	tests := []struct {
		name    string
		outputs []Output
		ref     string
	}{
		{"reference then change", []Output{{nullData(append([]byte("omni"), payload...)), 0}, {p2pkh(r), 5460}, {p2pkh(a), 990_000}}, addr(DestKeyHash, r)},
		{"change then reference", []Output{{p2pkh(a), 990_000}, {nullData(append([]byte("omni"), payload...)), 0}, {p2sh(r), 5460}}, addr(DestScriptHash, r)},
		{"only change", []Output{{p2pkh(a), 990_000}, {nullData(append([]byte("omni"), payload...)), 0}}, addr(DestKeyHash, a)},
	}
	for _, tt := range tests {
		p, err := d.Parse(&Tx{Outputs: tt.outputs, Inputs: ins})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		// Class C takes the first input even though the second is larger.
		if p.Class != ClassC || p.Sender != addr(DestKeyHash, a) || p.Reference != tt.ref || !bytes.Equal(p.Payload, payload) {
			t.Fatalf("%s: got %+v", tt.name, p)
		}
		var out int64
		for _, o := range tt.outputs {
			out += o.Value
		}
		if p.Fee != 1_000_000-out {
			t.Fatalf("%s: fee %d", tt.name, p.Fee)
		}
	}
}

func TestClassCPayloadAcrossPushes(t *testing.T) {
	d := decoder(t)
	payload := simpleSend(3, 5)
	outs := []Output{
		{nullData(append([]byte("omni"), payload[:6]...), payload[6:]), 0},
		{nullData([]byte("other")), 0},
		{p2pkh(hash20(9)), 1000},
	}
	p, err := d.Parse(&Tx{Outputs: outs, Inputs: []Output{{p2pkh(hash20(1)), 5000}}})
	if err != nil || !bytes.Equal(p.Payload, payload) {
		t.Fatalf("payload %x, err %v", p.Payload, err)
	}
}

// encodeClassB builds the multisig outputs like OmniCore_Encode_ClassB.
func encodeClassB(sender string, senderKey, payload []byte) []Output {
	hashes := obfuscationHashes(sender, maxObfuscation)
	var outs []Output
	var keys [][]byte
	for i, seq := 0, 1; i < len(payload); seq++ {
		n := min(len(payload)-i, packetSize-1)
		packet := make([]byte, packetSize)
		packet[0] = byte(seq)
		copy(packet[1:], payload[i:i+n])
		i += n
		for j := range packet {
			packet[j] ^= hashes[seq-1][j]
		}
		keys = append(keys, append(append([]byte{2}, packet...), 0))
	}
	for len(keys) > 0 {
		n := min(len(keys), 2)
		outs = append(outs, Output{multisig(1, append([][]byte{senderKey}, keys[:n]...)...), 7000})
		keys = keys[n:]
	}
	return outs
}

func TestClassB(t *testing.T) {
	d := decoder(t)
	a, b, r := hash20(0x11), hash20(0x22), hash20(0x33)
	sender := addr(DestKeyHash, a)
	senderKey := append([]byte{3}, bytes.Repeat([]byte{5}, 32)...)

	// 100 bytes need four packets in two multisig outputs.
	payload := make([]byte, 100)
	copy(payload, simpleSend(1, 42))
	for i := 16; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	outs := append([]Output{{exodusScript(t), 6000}}, encodeClassB(sender, senderKey, payload)...)
	outs = append(outs, Output{p2pkh(r), 6000}, Output{p2pkh(a), 50_000})

	// Largest input sum: a has 1000+1500, b has 2000.
	ins := []Output{{p2pkh(a), 1000}, {p2pkh(b), 2000}, {p2sh(a), 400}, {p2pkh(a), 1500}}
	p, err := d.Parse(&Tx{Outputs: outs, Inputs: ins})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != ClassB || p.Sender != sender || p.Reference != addr(DestKeyHash, r) {
		t.Fatalf("got %+v", p)
	}
	want := append(payload, make([]byte, 4*(packetSize-1)-len(payload))...)
	if !bytes.Equal(p.Payload, want) {
		t.Fatalf("payload\n%x\nwant\n%x", p.Payload, want)
	}
	m, err := Interpret(p.Payload, p.Sender, p.Reference, mainnet)
	if err != nil || m.Type != TypeSimpleSend || m.Property != 1 || m.Amount != 42 {
		t.Fatalf("interpret: %+v %v", m, err)
	}
}

func TestClassA(t *testing.T) {
	d := decoder(t)
	a, r := hash20(0x44), hash20(0x55)
	data := make([]byte, 20)
	data[0] = 1 // sequence number
	copy(data[1:], simpleSend(1, 777))

	refHash := append([]byte{2}, hash20(0x55)[1:]...)
	outs := []Output{{exodusScript(t), 6000}, {p2pkh(data), 6000}, {p2pkh(refHash), 6000}, {p2pkh(a), 9000}}
	p, err := d.Parse(&Tx{Outputs: outs, Inputs: []Output{{p2pkh(a), 50_000}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != ClassA || p.Reference != addr(DestKeyHash, refHash) || !bytes.Equal(p.Payload, data[1:]) {
		t.Fatalf("got %+v", p)
	}

	// Without a sequence match the reference is the output with the same
	// value as the data output and an Exodus output.
	outs[2] = Output{p2pkh(r), 6000}
	if p, err = d.Parse(&Tx{Outputs: outs, Inputs: []Output{{p2pkh(a), 50_000}}}); err != nil || p.Reference != addr(DestKeyHash, r) {
		t.Fatalf("value match: %+v %v", p, err)
	}

	// Exodus output without data: a possible DEx payment.
	seller := hash20(0x66)
	outs = []Output{{exodusScript(t), 5460}, {p2pkh(seller), 1_000_000}, {p2pkh(a), 9000}}
	p, err = d.Parse(&Tx{Outputs: outs, Inputs: []Output{{p2pkh(a), 2_000_000}}})
	if err != nil || !p.IsDExPaymentHint() {
		t.Fatalf("payment: %+v %v", p, err)
	}
	pays := d.DExPayments(outs)
	if len(pays) != 2 || pays[0].Vout != 1 || pays[0].Address != addr(DestKeyHash, seller) || pays[0].Value != 1_000_000 {
		t.Fatalf("payments %+v", pays)
	}
}

func TestSenderTieTakesSmallestAddress(t *testing.T) {
	d := decoder(t)
	x, y := addr(DestKeyHash, hash20(0x01)), addr(DestKeyHash, hash20(0x02))
	want := min(x, y)
	outs := []Output{{exodusScript(t), 6000}, {p2pkh(hash20(9)), 6000}}
	p, err := d.Parse(&Tx{Outputs: outs, Inputs: []Output{{p2pkh(hash20(0x02)), 1000}, {p2pkh(hash20(0x01)), 1000}}})
	if err != nil || p.Sender != want {
		t.Fatalf("sender %v, want %s (%v)", p, want, err)
	}
}

func TestRejectedInputs(t *testing.T) {
	d := decoder(t)
	classC := []Output{{nullData(append([]byte("omni"), simpleSend(1, 1)...)), 0}}
	classA := []Output{{exodusScript(t), 6000}, {p2pkh(hash20(9)), 6000}}
	tests := []struct {
		outs []Output
		ins  []Output
		code int
	}{
		{classC, []Output{{p2wpkh(hash20(1)), 1000}}, -109},
		{classC, []Output{{[]byte{0x51}, 1000}}, -108},
		{classA, []Output{{p2pkh(hash20(1)), 1000}, {p2wpkh(hash20(2)), 1000}}, -105},
		{classA, []Output{{p2pkh(hash20(1)), 0}}, -5},
	}
	for _, tt := range tests {
		_, err := d.Parse(&Tx{Outputs: tt.outs, Inputs: tt.ins})
		var rej *RejectError
		if !errors.As(err, &rej) || rej.Code != tt.code {
			t.Errorf("err = %v, want code %d", err, tt.code)
		}
	}
	// No marker: not an Omni transaction at all.
	if p, err := d.Parse(&Tx{Outputs: []Output{{p2pkh(hash20(1)), 1}}, Inputs: []Output{{p2pkh(hash20(2)), 2}}}); p != nil || err != nil {
		t.Fatalf("got %+v %v", p, err)
	}
}

func header(version, typ uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, version)
	binary.BigEndian.PutUint16(b[2:], typ)
	return b
}

func TestInterpretCreatePropertyFixed(t *testing.T) {
	p := header(0, TypeCreatePropertyFixed)
	p = append(p, 1, 0, 2, 0, 0, 0, 0) // ecosystem, divisible, previous id
	long := strings.Repeat("x", 300)
	for _, s := range []string{"Games", "Tokens", long, "https://liteworlds.quest", ""} {
		p = append(append(p, s...), 0)
	}
	p = binary.BigEndian.AppendUint64(p, 21_000_000)

	m, err := Interpret(p, "Lsender", "", mainnet)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ecosystem != 1 || m.PropType != 2 || m.Category != "Games" || m.Subcategory != "Tokens" ||
		len(m.Name) != 255 || m.URL != "https://liteworlds.quest" || m.Amount != 21_000_000 {
		t.Fatalf("got %+v", m)
	}
	// A cut-off amount reads past the payload.
	if _, err := Interpret(p[:len(p)-1], "Lsender", "", mainnet); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("truncated: err = %v", err)
	}
}

func TestInterpretFreezeAddressVersion(t *testing.T) {
	h := hash20(0x77)
	build := func(version byte) []byte {
		p := append(header(0, TypeFreezeTokens), simpleSend(5, 10)[4:]...)
		return append(append(p, version), h...)
	}
	m, err := Interpret(build(mainnet.ScriptPrefix), "Lsender", "Lref", mainnet)
	if err != nil || m.Receiver != addr(DestScriptHash, h) {
		t.Fatalf("legacy P2SH version: %+v %v", m, err)
	}
	m, err = Interpret(build(mainnet.PubKeyPrefix), "Lsender", "Lref", mainnet)
	if err != nil || m.Receiver != addr(DestKeyHash, h) {
		t.Fatalf("P2PKH version: %+v %v", m, err)
	}
	// OmniLite's HashToAddress does not know the "M..." version byte.
	if _, err := Interpret(build(mainnet.ScriptPrefix2), "Lsender", "Lref", mainnet); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("M version: err = %v", err)
	}
}

func TestInterpretMisc(t *testing.T) {
	sto1 := append(header(1, TypeSendToOwners), simpleSend(3, 9)[4:]...)
	tests := []struct {
		name    string
		payload []byte
		ok      bool
		check   func(*Message) bool
	}{
		{"short", []byte{0, 0, 0}, false, nil},
		{"unknown type", append(header(0, TypeRestrictedSend), make([]byte, 20)...), false, nil},
		{"send to self", simpleSend(1, 5), true, func(m *Message) bool { return m.Receiver == "Lsender" }},
		{"sto v1 too short", sto1, false, nil},
		{"sto v1", binary.BigEndian.AppendUint32(sto1, 7), true, func(m *Message) bool { return m.DistributionProperty == 7 }},
		{"grant without data", append(header(0, TypeGrantTokens), simpleSend(4, 1)[4:]...), true, func(m *Message) bool { return m.NFTData == "" && m.Receiver == "Lsender" }},
		{"any data", header(0, TypeAnyData), true, nil},
		{"dex offer v1", append(append(header(1, TypeTradeOffer), make([]byte, 29)...), 3), true, func(m *Message) bool { return m.Subaction == 3 }},
	}
	for _, tt := range tests {
		m, err := Interpret(tt.payload, "Lsender", "", mainnet)
		if (err == nil) != tt.ok || (tt.check != nil && err == nil && !tt.check(m)) {
			t.Errorf("%s: %+v %v", tt.name, m, err)
		}
	}
}
