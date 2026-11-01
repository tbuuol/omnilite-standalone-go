package omni

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

// Class is the encoding class of an Omni transaction.
type Class int

const (
	NoMarker Class = 0
	ClassA   Class = 1 // data in a fake P2PKH output, Exodus output
	ClassB   Class = 2 // obfuscated data in bare multisig, Exodus output
	ClassC   Class = 3 // data in OP_RETURN behind the "omni" marker
)

func (c Class) String() string {
	switch c {
	case ClassA:
		return "A"
	case ClassB:
		return "B"
	case ClassC:
		return "C"
	}
	return "-"
}

// Sizes from src/omnicore/parsing.h.
const (
	packetSizeClassA = 19
	packetSize       = 31
	maxPackets       = 255
	maxPayload       = maxPackets * packetSize
	maxObfuscation   = 255
)

var marker = []byte("omni")

// Output is a transaction output.
type Output struct {
	Script []byte
	Value  int64 // in litoshi
}

// Tx is a transaction with its spent outputs resolved.
type Tx struct {
	Txid    string
	Outputs []Output
	// Inputs holds the output spent by each input, in input order.
	Inputs []Output
}

// Parsed is a transaction carrying an Omni marker that passed the parser.
type Parsed struct {
	Class     Class
	Sender    string
	Reference string
	// Payload is the raw Omni payload. It is empty for Class A transactions
	// without a data output, which may be DEx payments.
	Payload []byte
	Fee     int64
}

// IsDExPaymentHint reports whether the transaction may pay for a DEx
// accept (parseTransaction returns 1 for these).
func (p *Parsed) IsDExPaymentHint() bool { return p.Class == ClassA && len(p.Payload) == 0 }

// RejectError tells why a transaction with a marker was dropped. Code is the
// return value of parseTransaction in OmniLite.
type RejectError struct {
	Code   int
	Reason string
}

func (e *RejectError) Error() string { return fmt.Sprintf("parse %d: %s", e.Code, e.Reason) }

// Decoder decodes transactions of one network.
type Decoder struct {
	Params *chain.Params
	exodus Destination
	// exodusAddr is the Exodus address string, compared against encoded
	// output addresses like exodus_address in omnicore.cpp.
	exodusAddr string
}

func NewDecoder(p *chain.Params) (*Decoder, error) {
	d, err := DecodeAddress(p.Exodus, p)
	if err != nil {
		return nil, fmt.Errorf("exodus address %s: %w", p.Exodus, err)
	}
	return &Decoder{Params: p, exodus: d, exodusAddr: p.Exodus}, nil
}

// HasMarker is a cheap pre-check on the raw output scripts: it is true for
// every transaction EncodingClass could classify, and for some more.
func (d *Decoder) HasMarker(outputs []Output) bool {
	for _, o := range outputs {
		if bytes.Contains(o.Script, marker) {
			return true
		}
		if dest, ok := extractDestination(o.Script); ok && dest == d.exodus {
			return true
		}
	}
	return false
}

func (d *Decoder) isExodus(script []byte) bool {
	t, sol := Solve(script)
	return t == PubKeyHash && d.exodus.Kind == DestKeyHash && bytes.Equal(sol[0], d.exodus.Hash[:])
}

// classCData returns the pushes of an OP_RETURN output whose first push
// starts with the "omni" marker, with the marker stripped.
func classCData(script []byte) ([][]byte, bool) {
	pushes, ok := scriptPushes(script, false)
	if !ok || len(pushes) == 0 || !bytes.HasPrefix(pushes[0], marker) {
		return nil, false
	}
	pushes[0] = pushes[0][len(marker):]
	return pushes, true
}

// EncodingClass mirrors GetEncodingClass.
func (d *Decoder) EncodingClass(outputs []Output) Class {
	var hasExodus, hasMultisig, hasOpReturn bool
	for _, o := range outputs {
		t, _ := Solve(o.Script)
		if !allowedOutput(t) {
			continue
		}
		switch t {
		case PubKeyHash:
			if d.isExodus(o.Script) {
				hasExodus = true
			}
		case Multisig:
			hasMultisig = true
		case NullData:
			if _, ok := classCData(o.Script); ok {
				hasOpReturn = true
			}
		}
	}
	switch {
	case hasOpReturn:
		return ClassC
	case hasExodus && hasMultisig:
		return ClassB
	case hasExodus:
		return ClassA
	}
	return NoMarker
}

// Parse mirrors parseTransaction up to the point where the payload is
// handed to the interpreter. It returns nil, nil for transactions without a
// marker.
func (d *Decoder) Parse(tx *Tx) (*Parsed, error) {
	class := d.EncodingClass(tx.Outputs)
	if class == NoMarker {
		return nil, nil
	}
	if len(tx.Inputs) == 0 {
		return nil, &RejectError{-101, "no inputs"}
	}

	sender, err := d.sender(class, tx.Inputs)
	if err != nil {
		return nil, err
	}
	var in, out int64
	for _, o := range tx.Inputs {
		in += o.Value
	}
	for _, o := range tx.Outputs {
		out += o.Value
	}
	res := &Parsed{Class: class, Sender: sender, Fee: in - out}

	// Data population: addresses, hashes and values of all P2PKH and P2SH
	// outputs except the Exodus output.
	var addrs []string
	var hashes [][]byte
	var values []int64
	for _, o := range tx.Outputs {
		t, sol := Solve(o.Script)
		if t != PubKeyHash && t != ScriptHash {
			continue // other allowed types have no single destination
		}
		dest, _ := extractDestination(o.Script)
		if dest == d.exodus {
			continue
		}
		addrs = append(addrs, dest.Encode(d.Params))
		hashes = append(hashes, sol[0])
		values = append(values, o.Value)
	}

	switch class {
	case ClassA:
		res.Reference, res.Payload = d.parseClassA(tx.Outputs, addrs, hashes, values)
	case ClassB, ClassC:
		res.Reference = d.referenceBC(addrs, sender)
		if class == ClassB {
			res.Payload = classBPayload(tx.Outputs, sender)
		} else {
			res.Payload = classCPayload(tx.Outputs)
		}
	}
	return res, nil
}

// sender picks the sending address: for Class C the first input, otherwise
// the address with the largest input sum.
func (d *Decoder) sender(class Class, inputs []Output) (string, error) {
	addrOf := func(o Output) (string, bool) {
		dest, ok := extractDestination(o.Script)
		if !ok {
			return "", false
		}
		return dest.Encode(d.Params), true
	}

	if class == ClassC {
		t, _ := Solve(inputs[0].Script)
		if t == NonStandard {
			return "", &RejectError{-108, "first input is not a standard script"}
		}
		if !allowedInput(t) {
			return "", &RejectError{-109, "first input type is not allowed"}
		}
		addr, ok := addrOf(inputs[0])
		if !ok {
			return "", &RejectError{-110, "first input has no address"}
		}
		return addr, nil
	}

	sums := map[string]int64{}
	for _, o := range inputs {
		t, _ := Solve(o.Script)
		if t == NonStandard {
			return "", &RejectError{-104, "input is not a standard script"}
		}
		if !allowedInput(t) {
			return "", &RejectError{-105, "input type is not allowed"}
		}
		addr, ok := addrOf(o)
		if !ok {
			return "", &RejectError{-106, "input has no address"}
		}
		sums[addr] += o.Value
	}
	// std::map iterates in key order; the first strictly larger sum wins.
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sender string
	var max int64
	for _, k := range keys {
		if sums[k] > max {
			sender, max = k, sums[k]
		}
	}
	if sender == "" {
		return "", &RejectError{-5, "no sender"}
	}
	return sender, nil
}

func (d *Decoder) parseClassA(outputs []Output, addrs []string, hashes [][]byte, values []int64) (reference string, payload []byte) {
	one := []byte{0, 0, 0, 0, 0, 0, 0, 1}
	two := []byte{0, 0, 0, 0, 0, 0, 0, 2}

	// Step 1: the data output, whose bytes 1..8 look like a simple send of
	// property 1 or 2. More than one candidate invalidates Class A.
	dataIdx := -1
	for k, h := range hashes {
		if bytes.Equal(h[1:9], one) || bytes.Equal(h[1:9], two) {
			if dataIdx >= 0 {
				return "", nil
			}
			dataIdx = k
		}
	}
	if dataIdx < 0 {
		return "", nil
	}
	dataAddr := addrs[dataIdx]

	// Step 2: the reference is the output with the next sequence number.
	expected := hashes[dataIdx][0] + 1
	for k, h := range hashes {
		if addrs[k] != dataAddr && addrs[k] != d.exodusAddr && h[0] == expected {
			if reference != "" {
				reference = ""
				break
			}
			reference = addrs[k]
		}
	}

	// Step 3: otherwise an output with the same value as the data output
	// and an Exodus output.
	if reference == "" {
		var exodusValues []int64
		for _, o := range outputs {
			if dest, ok := extractDestination(o.Script); ok && dest == d.exodus {
				exodusValues = append(exodusValues, o.Value)
			}
		}
		for k := range hashes {
			if addrs[k] == dataAddr || addrs[k] == d.exodusAddr || values[k] != values[dataIdx] {
				continue
			}
			for _, ev := range exodusValues {
				if values[k] != ev {
					continue
				}
				if reference == "" {
					reference = addrs[k]
				} else {
					// Only the inner loop is left here in OmniLite; the
					// next output can set the reference again.
					reference = ""
					break
				}
			}
		}
	}
	if reference == "" {
		return "", nil
	}
	return reference, append([]byte(nil), hashes[dataIdx][1:1+packetSizeClassA]...)
}

// referenceBC finds the reference of a Class B or C transaction: the only
// candidate output, or else the last one, skipping the first output back to
// the sender as change.
func (d *Decoder) referenceBC(addrs []string, sender string) string {
	var candidates []string
	for _, a := range addrs {
		if a != d.exodusAddr {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	reference := ""
	changeRemoved := false
	for _, a := range candidates {
		if a == sender && !changeRemoved {
			changeRemoved = true
			continue
		}
		reference = a
	}
	return reference
}

// obfuscationHashes returns SHA256 chained over the upper-case hex string,
// seeded with the sender address (PrepareObfuscatedHashes).
func obfuscationHashes(seed string, count int) [][]byte {
	if count > maxObfuscation {
		count = maxObfuscation
	}
	hashes := make([][]byte, 0, count)
	in := seed
	for i := 0; i < count; i++ {
		h := sha256.Sum256([]byte(in))
		hashes = append(hashes, h[:])
		in = strings.ToUpper(hex.EncodeToString(h[:]))
	}
	return hashes
}

func classBPayload(outputs []Output, sender string) []byte {
	var keys [][]byte
	for _, o := range outputs {
		if t, _ := Solve(o.Script); t != Multisig {
			continue
		}
		// The first key belongs to the sender and carries no data.
		pushes, _ := scriptPushes(o.Script, true)
		keys = append(keys, pushes...)
	}
	if len(keys) > maxPackets {
		keys = keys[:maxPackets]
	}
	// Packet k is obfuscated with the (k+1)-th chained hash of the sender.
	hashes := obfuscationHashes(sender, len(keys))
	payload := make([]byte, 0, len(keys)*(packetSize-1))
	for k, key := range keys {
		packet := make([]byte, packetSize)
		copy(packet, key[1:])
		for i := range packet {
			packet[i] ^= hashes[k][i]
		}
		// The first byte is a sequence number, which is not checked.
		payload = append(payload, packet[1:]...)
	}
	return payload
}

func classCPayload(outputs []Output) []byte {
	var payload []byte
	for _, o := range outputs {
		if t, _ := Solve(o.Script); t != NullData {
			continue
		}
		pushes, ok := classCData(o.Script)
		if !ok {
			continue
		}
		for _, p := range pushes {
			if len(payload)+len(p) > maxPayload {
				p = p[:maxPayload-len(payload)]
			}
			payload = append(payload, p...)
			if len(payload) == maxPayload {
				return payload
			}
		}
	}
	return payload
}

// DExPayment is an output of a possible DEx payment.
type DExPayment struct {
	Vout    int    `json:"vout"`
	Address string `json:"address"`
	Value   int64  `json:"value"`
}

// DExPayments lists the outputs HandleDExPayments checks against open
// accepts: every output with an address except the Exodus output.
func (d *Decoder) DExPayments(outputs []Output) []DExPayment {
	var res []DExPayment
	for n, o := range outputs {
		dest, ok := extractDestination(o.Script)
		if !ok || dest == d.exodus {
			continue
		}
		res = append(res, DExPayment{Vout: n, Address: dest.Encode(d.Params), Value: o.Value})
	}
	return res
}
