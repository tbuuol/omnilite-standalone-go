package wallet

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

// Tx is a raw Litecoin transaction (without MWEB data, which Omni never uses).
type Tx struct {
	Version  int32
	Inputs   []TxIn
	Outputs  []TxOut
	LockTime uint32
}

type TxIn struct {
	Txid     string // display byte order
	Vout     uint32
	Script   []byte
	Sequence uint32
	Witness  [][]byte
}

type TxOut struct {
	Value  int64
	Script []byte
}

// NewTx is an empty transaction like CMutableTransaction.
func NewTx() *Tx { return &Tx{Version: 2} }

func (t *Tx) hasWitness() bool {
	for _, in := range t.Inputs {
		if len(in.Witness) > 0 {
			return true
		}
	}
	return false
}

// Serialize encodes the transaction, with witness data if there is any.
func (t *Tx) Serialize() []byte { return t.serialize(t.hasWitness()) }

func (t *Tx) serialize(witness bool) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, t.Version)
	if witness {
		b.Write([]byte{0, 1})
	}
	writeVarInt(&b, uint64(len(t.Inputs)))
	for _, in := range t.Inputs {
		h, _ := hex.DecodeString(in.Txid)
		b.Write(reverse(h))
		binary.Write(&b, binary.LittleEndian, in.Vout)
		writeBytes(&b, in.Script)
		binary.Write(&b, binary.LittleEndian, in.Sequence)
	}
	writeVarInt(&b, uint64(len(t.Outputs)))
	for _, o := range t.Outputs {
		binary.Write(&b, binary.LittleEndian, o.Value)
		writeBytes(&b, o.Script)
	}
	if witness {
		for _, in := range t.Inputs {
			writeVarInt(&b, uint64(len(in.Witness)))
			for _, w := range in.Witness {
				writeBytes(&b, w)
			}
		}
	}
	binary.Write(&b, binary.LittleEndian, t.LockTime)
	return b.Bytes()
}

// Hex returns the serialized transaction as hex.
func (t *Tx) Hex() string { return hex.EncodeToString(t.Serialize()) }

// Txid is the hash of the serialization without witness data.
func (t *Tx) Txid() string {
	h1 := sha256.Sum256(t.serialize(false))
	h2 := sha256.Sum256(h1[:])
	return hex.EncodeToString(reverse(h2[:]))
}

// ParseTx decodes a hex transaction like DecodeHexTx: the witness format
// is tried first, then the legacy one, and no bytes may be left over.
func ParseTx(s string) (*Tx, error) {
	if s == "" {
		return NewTx(), nil
	}
	data, err := hex.DecodeString(s)
	if err != nil {
		return nil, errDeserialize
	}
	if t, err := parseTx(data, true); err == nil {
		return t, nil
	}
	return parseTx(data, false)
}

var errDeserialize = errors.New("Transaction deserialization failed")

func parseTx(data []byte, allowWitness bool) (*Tx, error) {
	r := bytes.NewReader(data)
	t := &Tx{}
	if err := binary.Read(r, binary.LittleEndian, &t.Version); err != nil {
		return nil, errDeserialize
	}
	nIn, err := readVarInt(r)
	if err != nil {
		return nil, errDeserialize
	}
	witness := false
	if nIn == 0 && allowWitness {
		flag, err := r.ReadByte()
		if err != nil || flag != 1 {
			return nil, errDeserialize
		}
		witness = true
		if nIn, err = readVarInt(r); err != nil {
			return nil, errDeserialize
		}
	}
	if nIn > uint64(len(data)) {
		return nil, errDeserialize
	}
	for i := uint64(0); i < nIn; i++ {
		var h [32]byte
		var in TxIn
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, errDeserialize
		}
		in.Txid = hex.EncodeToString(reverse(h[:]))
		if binary.Read(r, binary.LittleEndian, &in.Vout) != nil {
			return nil, errDeserialize
		}
		if in.Script, err = readBytes(r); err != nil {
			return nil, errDeserialize
		}
		if binary.Read(r, binary.LittleEndian, &in.Sequence) != nil {
			return nil, errDeserialize
		}
		t.Inputs = append(t.Inputs, in)
	}
	nOut, err := readVarInt(r)
	if err != nil || nOut > uint64(len(data)) {
		return nil, errDeserialize
	}
	for i := uint64(0); i < nOut; i++ {
		var o TxOut
		if binary.Read(r, binary.LittleEndian, &o.Value) != nil {
			return nil, errDeserialize
		}
		if o.Script, err = readBytes(r); err != nil {
			return nil, errDeserialize
		}
		t.Outputs = append(t.Outputs, o)
	}
	if witness {
		for i := range t.Inputs {
			n, err := readVarInt(r)
			if err != nil || n > uint64(len(data)) {
				return nil, errDeserialize
			}
			for j := uint64(0); j < n; j++ {
				w, err := readBytes(r)
				if err != nil {
					return nil, errDeserialize
				}
				t.Inputs[i].Witness = append(t.Inputs[i].Witness, w)
			}
		}
		if !t.hasWitness() {
			return nil, errDeserialize // superfluous witness record
		}
	}
	if binary.Read(r, binary.LittleEndian, &t.LockTime) != nil || r.Len() != 0 {
		return nil, errDeserialize
	}
	return t, nil
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

func varIntSize(n uint64) int {
	switch {
	case n < 0xfd:
		return 1
	case n <= 0xffff:
		return 3
	case n <= 0xffffffff:
		return 5
	}
	return 9
}

func writeVarInt(b *bytes.Buffer, n uint64) {
	switch {
	case n < 0xfd:
		b.WriteByte(byte(n))
	case n <= 0xffff:
		b.WriteByte(0xfd)
		binary.Write(b, binary.LittleEndian, uint16(n))
	case n <= 0xffffffff:
		b.WriteByte(0xfe)
		binary.Write(b, binary.LittleEndian, uint32(n))
	default:
		b.WriteByte(0xff)
		binary.Write(b, binary.LittleEndian, n)
	}
}

func writeBytes(b *bytes.Buffer, data []byte) {
	writeVarInt(b, uint64(len(data)))
	b.Write(data)
}

func readVarInt(r *bytes.Reader) (uint64, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	switch c {
	case 0xfd:
		var v uint16
		err = binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 0xfe:
		var v uint32
		err = binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 0xff:
		var v uint64
		err = binary.Read(r, binary.LittleEndian, &v)
		return v, err
	}
	return uint64(c), nil
}

func readBytes(r *bytes.Reader) ([]byte, error) {
	n, err := readVarInt(r)
	if err != nil {
		return nil, err
	}
	if n > uint64(r.Len()) {
		return nil, io.ErrUnexpectedEOF
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return b, err
}
