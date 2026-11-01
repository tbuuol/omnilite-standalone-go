package omni

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math/big"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
	"golang.org/x/crypto/ripemd160" //nolint:staticcheck // Hash160 is part of the address format.
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func doubleSHA256(b []byte) []byte {
	h := sha256.Sum256(b)
	h = sha256.Sum256(h[:])
	return h[:]
}

// Hash160 is RIPEMD160(SHA256(b)).
func Hash160(b []byte) []byte {
	s := sha256.Sum256(b)
	r := ripemd160.New()
	r.Write(s[:])
	return r.Sum(nil)
}

func base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	var out []byte
	mod, radix := new(big.Int), big.NewInt(58)
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	n := new(big.Int)
	radix := big.NewInt(58)
	for _, c := range []byte(s) {
		i := bytes.IndexByte([]byte(base58Alphabet), c)
		if i < 0 {
			return nil, errors.New("invalid base58 character")
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(i)))
	}
	out := n.Bytes()
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), out...), nil
}

func base58CheckEncode(version byte, payload []byte) string {
	b := append([]byte{version}, payload...)
	return base58Encode(append(b, doubleSHA256(b)[:4]...))
}

// DestKind is the kind of a base58 destination.
type DestKind byte

const (
	DestKeyHash    DestKind = 1 // P2PKH ("L..." on mainnet)
	DestScriptHash DestKind = 2 // P2SH ("M..." on mainnet)
)

// Destination is a P2PKH or P2SH destination: the only kinds OmniLite
// accepts as senders and references.
type Destination struct {
	Kind DestKind
	Hash [20]byte
}

// Encode returns the address like Litecoin's EncodeDestination: P2SH is
// encoded with SCRIPT_ADDRESS2 ("M..."), never with the legacy "3..." prefix.
func (d Destination) Encode(p *chain.Params) string {
	if d.Kind == DestKeyHash {
		return base58CheckEncode(p.PubKeyPrefix, d.Hash[:])
	}
	return base58CheckEncode(p.ScriptPrefix2, d.Hash[:])
}

// DecodeAddress parses a base58 address of the network. Both P2SH prefixes
// are accepted, as Litecoin's DecodeDestination does.
func DecodeAddress(addr string, p *chain.Params) (Destination, error) {
	b, err := base58Decode(addr)
	if err != nil {
		return Destination{}, err
	}
	if len(b) != 25 || !bytes.Equal(doubleSHA256(b[:21])[:4], b[21:]) {
		return Destination{}, errors.New("invalid address checksum")
	}
	var d Destination
	copy(d.Hash[:], b[1:21])
	switch b[0] {
	case p.PubKeyPrefix:
		d.Kind = DestKeyHash
	case p.ScriptPrefix, p.ScriptPrefix2:
		d.Kind = DestScriptHash
	default:
		return Destination{}, errors.New("address is not for this network")
	}
	return d, nil
}

// hashToAddress mirrors HashToAddress in parsing.cpp: only PUBKEY_ADDRESS
// and the legacy SCRIPT_ADDRESS version bytes are recognised. A payload with
// the "M..." version byte yields no address.
func hashToAddress(version byte, hash []byte, p *chain.Params) string {
	var d Destination
	copy(d.Hash[:], hash)
	switch version {
	case p.PubKeyPrefix:
		d.Kind = DestKeyHash
	case p.ScriptPrefix:
		d.Kind = DestScriptHash
	default:
		return ""
	}
	return d.Encode(p)
}

// validPubKey mirrors CPubKey::IsValid, which only checks that the length
// fits the header byte.
func validPubKey(k []byte) bool {
	if len(k) == 0 {
		return false
	}
	switch k[0] {
	case 2, 3:
		return len(k) == 33
	case 4, 6, 7:
		return len(k) == 65
	}
	return false
}

// extractDestination mirrors ExtractDestination for the destination kinds
// that can be Omni addresses. Pay-to-pubkey outputs map to the key's P2PKH
// address. Witness outputs yield no destination here: they can never be an
// Omni sender, so they never match a DEx offer.
func extractDestination(script []byte) (Destination, bool) {
	t, sol := Solve(script)
	var d Destination
	switch t {
	case PubKey:
		if !validPubKey(sol[0]) {
			return d, false
		}
		d.Kind = DestKeyHash
		copy(d.Hash[:], Hash160(sol[0]))
	case PubKeyHash:
		d.Kind = DestKeyHash
		copy(d.Hash[:], sol[0])
	case ScriptHash:
		d.Kind = DestScriptHash
		copy(d.Hash[:], sol[0])
	default:
		return d, false
	}
	return d, true
}
