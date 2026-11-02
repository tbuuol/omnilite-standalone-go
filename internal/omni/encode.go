package omni

import (
	"crypto/rand"
	"errors"
	"math/big"
)

// Output scripts for building transactions (encoding.cpp, script.cpp).

// MaxDatacarrierBytes is MAX_OP_RETURN_RELAY, Litecoin Core's default
// -datacarriersize: the whole OP_RETURN script.
const MaxDatacarrierBytes = 83

// UseClassC mirrors UseEncodingClassC as WalletTxBuilder calls it: the
// payload plus marker, OP_RETURN and two pushdata bytes must fit the data
// carrier size.
func UseClassC(payloadSize int) bool {
	return payloadSize+1+2+len(marker) <= MaxDatacarrierBytes
}

// ScriptForDestination returns the P2PKH or P2SH output script.
func ScriptForDestination(d Destination) []byte {
	if d.Kind == DestKeyHash {
		s := []byte{opDup, opHash160, 20}
		s = append(s, d.Hash[:]...)
		return append(s, opEqualVerify, opCheckSig)
	}
	s := []byte{opHash160, 20}
	s = append(s, d.Hash[:]...)
	return append(s, opEqual)
}

// pushData appends a minimal push of data, like CScript << vector.
func pushData(s, data []byte) []byte {
	switch n := len(data); {
	case n < opPushData1:
		s = append(s, byte(n))
	case n <= 0xff:
		s = append(s, opPushData1, byte(n))
	default:
		s = append(s, opPushData2, byte(n), byte(n>>8))
	}
	return append(s, data...)
}

// ClassCScript mirrors OmniCore_Encode_ClassC: OP_RETURN with marker and
// payload in one push. It fails if the data does not fit.
func ClassCScript(payload []byte) ([]byte, error) {
	data := append(append([]byte(nil), marker...), payload...)
	if len(data) > MaxDatacarrierBytes {
		return nil, errors.New("payload too large for class C")
	}
	return pushData([]byte{opReturn}, data), nil
}

// MultisigScript mirrors GetScriptForMultisig.
func MultisigScript(required int, keys [][]byte) []byte {
	s := []byte{byte(op1 + required - 1)}
	for _, k := range keys {
		s = pushData(s, k)
	}
	return append(s, byte(op1+len(keys)-1), opCheckMultisig)
}

// ClassBScripts mirrors OmniCore_Encode_ClassB without the Exodus output:
// the payload in 31-byte packets, obfuscated with the sender's hash chain,
// packed as fake keys into 1-of-n bare multisig outputs beside the
// redeeming key.
func ClassBScripts(sender string, redeemKey, payload []byte) ([][]byte, error) {
	if !FullyValidPubKey(redeemKey) {
		return nil, errors.New("invalid redemption key")
	}
	if len(payload) > maxObfuscation*(packetSize-1) {
		return nil, errors.New("payload too large for class B")
	}
	hashes := obfuscationHashes(sender, maxObfuscation)
	var scripts [][]byte
	seq := 1
	for next := 0; next < len(payload); {
		nKeys := 1
		if len(payload)-next > packetSize-1 {
			nKeys = 2
		}
		keys := [][]byte{redeemKey}
		for i := 0; i < nKeys; i++ {
			n := min(len(payload)-next, packetSize-1)
			packet := make([]byte, packetSize)
			packet[0] = byte(seq)
			copy(packet[1:], payload[next:next+n])
			next += n
			for j := range packet {
				packet[j] ^= hashes[seq-1][j]
			}
			key := make([]byte, 33)
			key[0] = 0x02
			copy(key[1:], packet)
			// Fix the last byte so the key is a point on the curve.
			var r [1]byte
			if _, err := rand.Read(r[:]); err != nil {
				return nil, err
			}
			for j := 0; j < 256; j++ {
				key[32] = r[0]
				if FullyValidPubKey(key) {
					break
				}
				r[0]++
			}
			keys = append(keys, key)
			seq++
		}
		scripts = append(scripts, MultisigScript(1, keys))
	}
	return scripts, nil
}

var (
	curveP = func() *big.Int {
		p, _ := new(big.Int).SetString("fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2f", 16)
		return p
	}()
	curveB = big.NewInt(7)
)

// FullyValidPubKey mirrors CPubKey::IsFullyValid for compressed and
// uncompressed keys: the key must decode to a point on secp256k1.
func FullyValidPubKey(k []byte) bool {
	if !validPubKey(k) {
		return false
	}
	x := new(big.Int).SetBytes(k[1:33])
	if x.Cmp(curveP) >= 0 {
		return false
	}
	// y² = x³ + 7
	rhs := new(big.Int).Exp(x, big.NewInt(3), curveP)
	rhs.Add(rhs, curveB).Mod(rhs, curveP)
	switch k[0] {
	case 2, 3:
		return rhs.Sign() == 0 || big.Jacobi(rhs, curveP) == 1
	default:
		y := new(big.Int).SetBytes(k[33:])
		if y.Cmp(curveP) >= 0 {
			return false
		}
		// Hybrid keys (06/07) also encode the parity of y.
		if k[0] != 4 && int(k[0]&1) != int(y.Bit(0)) {
			return false
		}
		lhs := new(big.Int).Mul(y, y)
		return lhs.Mod(lhs, curveP).Cmp(rhs) == 0
	}
}
