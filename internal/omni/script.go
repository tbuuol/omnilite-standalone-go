// Package omni decodes OmniLite transactions: it finds the encoding class,
// the sender and the reference, and extracts and interprets the payload.
// The rules follow OmniLite 0.18.1 (src/omnicore/omnicore.cpp parseTransaction,
// src/omnicore/script.cpp and src/omnicore/tx.cpp) byte for byte, since they
// are consensus.
package omni

import "encoding/binary"

// Script opcodes used by the decoder.
const (
	op0             = 0x00
	opPushData1     = 0x4c
	opPushData2     = 0x4d
	opPushData4     = 0x4e
	op1             = 0x51
	op16            = 0x60
	opReturn        = 0x6a
	opDup           = 0x76
	opEqual         = 0x87
	opEqualVerify   = 0x88
	opHash160       = 0xa9
	opCheckSig      = 0xac
	opCheckMultisig = 0xae
)

// ScriptType mirrors Litecoin Core's txnouttype as returned by OmniLite's
// SafeSolver.
type ScriptType int

const (
	NonStandard ScriptType = iota
	PubKey
	PubKeyHash
	ScriptHash
	Multisig
	NullData
	WitnessV0KeyHash
	WitnessV0ScriptHash
)

type scriptOp struct {
	code byte
	data []byte
}

// getOp reads the operation at script[pc:]. It fails like CScript::GetOp
// when a push runs past the end of the script.
func getOp(script []byte, pc int) (op scriptOp, next int, ok bool) {
	if pc >= len(script) {
		return op, pc, false
	}
	op.code = script[pc]
	pc++
	if op.code > opPushData4 {
		return op, pc, true
	}
	var n int
	switch {
	case op.code < opPushData1:
		n = int(op.code)
	case op.code == opPushData1:
		if len(script)-pc < 1 {
			return op, pc, false
		}
		n = int(script[pc])
		pc++
	case op.code == opPushData2:
		if len(script)-pc < 2 {
			return op, pc, false
		}
		n = int(binary.LittleEndian.Uint16(script[pc:]))
		pc += 2
	default:
		if len(script)-pc < 4 {
			return op, pc, false
		}
		u := binary.LittleEndian.Uint32(script[pc:])
		if uint64(u) > uint64(len(script)-pc-4) {
			return op, pc, false
		}
		n = int(u)
		pc += 4
	}
	if n < 0 || len(script)-pc < n {
		return op, pc, false
	}
	op.data = script[pc : pc+n]
	return op, pc + n, true
}

// scriptPushes returns the data of all push operations (OP_0 up to
// OP_PUSHDATA4), like GetScriptPushes. ok is false if the script cannot be
// parsed; the pushes read until then are still returned.
func scriptPushes(script []byte, skipFirst bool) (pushes [][]byte, ok bool) {
	count := 0
	for pc := 0; pc < len(script); {
		op, next, ok := getOp(script, pc)
		if !ok {
			return pushes, false
		}
		pc = next
		if op.code <= opPushData4 {
			if count > 0 || !skipFirst {
				pushes = append(pushes, op.data)
			}
			count++
		}
	}
	return pushes, true
}

func isPushOnly(script []byte) bool {
	for pc := 0; pc < len(script); {
		op, next, ok := getOp(script, pc)
		if !ok || op.code > op16 {
			return false
		}
		pc = next
	}
	return true
}

func isP2SH(script []byte) bool {
	return len(script) == 23 && script[0] == opHash160 && script[1] == 0x14 && script[22] == opEqual
}

func witnessProgram(script []byte) (version int, program []byte, ok bool) {
	if len(script) < 4 || len(script) > 42 {
		return 0, nil, false
	}
	if script[0] != op0 && (script[0] < op1 || script[0] > op16) {
		return 0, nil, false
	}
	if int(script[1])+2 != len(script) {
		return 0, nil, false
	}
	if script[0] != op0 {
		version = int(script[0]) - op1 + 1
	}
	return version, script[2:], true
}

func isSmallInt(code byte) bool { return code == op0 || (code >= op1 && code <= op16) }

func smallIntValue(code byte) int {
	if code == op0 {
		return 0
	}
	return int(code) - op1 + 1
}

func isPubKeySize(data []byte) bool { return len(data) >= 33 && len(data) <= 65 }

// Solve classifies a scriptPubKey like OmniLite's SafeSolver and returns the
// solutions (public key, hash or the multisig public keys). Template matching
// works on parsed operations, so non-minimal pushes still match.
func Solve(script []byte) (ScriptType, [][]byte) {
	if isP2SH(script) {
		return ScriptHash, [][]byte{script[2:22]}
	}
	if version, program, ok := witnessProgram(script); ok {
		switch {
		case version == 0 && len(program) == 20:
			return WitnessV0KeyHash, [][]byte{program}
		case version == 0 && len(program) == 32:
			return WitnessV0ScriptHash, [][]byte{program}
		}
		return NonStandard, nil
	}
	if len(script) >= 2 && script[0] == opReturn && isPushOnly(script[1:]) {
		return NullData, nil
	}

	var ops []scriptOp
	for pc := 0; pc < len(script); {
		op, next, ok := getOp(script, pc)
		if !ok {
			// Templates are compared op by op until the first unreadable
			// operation, which never completes a match.
			ops = append(ops, scriptOp{code: 0xff})
			break
		}
		ops = append(ops, op)
		pc = next
	}
	isPush := func(op scriptOp) bool { return op.code <= opPushData4 }

	// <pubkey> OP_CHECKSIG
	if len(ops) == 2 && isPush(ops[0]) && isPubKeySize(ops[0].data) && ops[1].code == opCheckSig {
		return PubKey, [][]byte{ops[0].data}
	}
	// OP_DUP OP_HASH160 <20 bytes> OP_EQUALVERIFY OP_CHECKSIG
	if len(ops) == 5 && ops[0].code == opDup && ops[1].code == opHash160 &&
		isPush(ops[2]) && len(ops[2].data) == 20 && ops[3].code == opEqualVerify && ops[4].code == opCheckSig {
		return PubKeyHash, [][]byte{ops[2].data}
	}
	// OP_m <pubkeys...> OP_n OP_CHECKMULTISIG
	if len(ops) >= 3 && isSmallInt(ops[0].code) {
		i := 1
		var keys [][]byte
		for i < len(ops) && isPush(ops[i]) && isPubKeySize(ops[i].data) {
			keys = append(keys, ops[i].data)
			i++
		}
		if i+2 == len(ops) && isSmallInt(ops[i].code) && ops[i+1].code == opCheckMultisig {
			m, n := smallIntValue(ops[0].code), smallIntValue(ops[i].code)
			if m >= 1 && n >= 1 && m <= n && len(keys) == n {
				return Multisig, keys
			}
		}
	}
	return NonStandard, nil
}

// allowedInput reports whether an input of this type may fund an Omni
// transaction (IsAllowedInputType). The activation heights in rules.cpp all
// lie at or below the Omni genesis block, which is where parsing starts, so
// they do not need to be checked.
func allowedInput(t ScriptType) bool { return t == PubKeyHash || t == ScriptHash }

// allowedOutput reports whether an output of this type is considered at all
// (IsAllowedOutputType). See allowedInput for the activation heights.
func allowedOutput(t ScriptType) bool {
	return t == PubKeyHash || t == ScriptHash || t == Multisig || t == NullData
}
