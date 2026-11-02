package wallet

// Fee and dust rules of Litecoin Core 0.18 (policy.cpp, feerate.cpp) and
// OmniLite (script.cpp).

// getFee mirrors CFeeRate::GetFee: litoshi for size bytes at feePerK per
// 1000 bytes, at least 1 litoshi for a positive rate.
func getFee(feePerK int64, size int) int64 {
	fee := feePerK * int64(size) / 1000
	if fee == 0 && size != 0 {
		if feePerK > 0 {
			fee = 1
		} else if feePerK < 0 {
			fee = -1
		}
	}
	return fee
}

// isUnspendable mirrors CScript::IsUnspendable.
func isUnspendable(script []byte) bool {
	return (len(script) > 0 && script[0] == 0x6a) || len(script) > 10000
}

// isWitnessProgram mirrors CScript::IsWitnessProgram.
func isWitnessProgram(script []byte) bool {
	if len(script) < 4 || len(script) > 42 {
		return false
	}
	if script[0] != 0 && (script[0] < 0x51 || script[0] > 0x60) {
		return false
	}
	return int(script[1])+2 == len(script)
}

// dustThreshold mirrors GetDustThreshold.
func dustThreshold(script []byte, feePerK int64) int64 {
	if isUnspendable(script) {
		return 0
	}
	size := 8 + varIntSize(uint64(len(script))) + len(script)
	if isWitnessProgram(script) {
		size += 32 + 4 + 1 + 107/4 + 4
	} else {
		size += 32 + 4 + 1 + 107 + 4
	}
	return getFee(feePerK, size)
}

// omniDust mirrors OmniGetDustThreshold: thirty times the relay dust.
func omniDust(script []byte, relayFee int64) int64 {
	return dustThreshold(script, relayFee) * 30
}

// economicThreshold mirrors GetEconomicThreshold: outputs below it are not
// worth spending.
func economicThreshold(script []byte, relayFee, feeRate int64) int64 {
	return max(dustThreshold(script, relayFee), dustThreshold(script, feeRate)/3)
}

// inputKind describes the size of a signed input.
type inputKind struct {
	base    int // bytes outside the witness
	witness int // witness bytes, including the item count
}

const sigSize = 72 // DER signature with sighash byte, as Core's dummy signer

func p2pkhInput(pubKeySize int) inputKind {
	return inputKind{base: 32 + 4 + 1 + (1 + sigSize + 1 + pubKeySize) + 4}
}

// p2shP2WPKHInput: the script sig pushes the 22-byte witness program.
var p2shP2WPKHInput = inputKind{base: 32 + 4 + 1 + 23 + 4, witness: 1 + 1 + sigSize + 1 + 33}

// virtualSize estimates the signed transaction's size in virtual bytes.
func virtualSize(t *Tx, inputs []inputKind) int {
	base := 4 + 4 + varIntSize(uint64(len(inputs))) + varIntSize(uint64(len(t.Outputs)))
	for _, o := range t.Outputs {
		base += 8 + varIntSize(uint64(len(o.Script))) + len(o.Script)
	}
	witness, segwit := 0, false
	for _, in := range inputs {
		base += in.base
		if in.witness > 0 {
			segwit = true
			witness += in.witness
		} else {
			witness++ // empty witness stack
		}
	}
	weight := base * 4
	if segwit {
		weight += 2 + witness
	}
	return (weight + 3) / 4
}
