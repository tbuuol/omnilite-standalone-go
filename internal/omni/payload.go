package omni

import (
	"encoding/binary"
	"errors"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

// Transaction types (src/omnicore/omnicore.h). Types without an
// interpreter in OmniLite are listed for naming only; payloads of these
// types are structurally invalid.
const (
	TypeSimpleSend          = 0
	TypeRestrictedSend      = 2
	TypeSendToOwners        = 3
	TypeSendAll             = 4
	TypeSendNonFungible     = 5
	TypeSavingsMark         = 10
	TypeSavingsCompromised  = 11
	TypeRateLimitedMark     = 12
	TypeAutomaticDispensary = 15
	TypeTradeOffer          = 20
	TypeAcceptOffer         = 22
	TypeNotification        = 31
	TypeOfferAcceptBet      = 40
	TypeCreatePropertyFixed = 50
	TypeCreatePropertyVar   = 51
	TypePromoteProperty     = 52
	TypeCloseCrowdsale      = 53
	TypeCreatePropertyMan   = 54
	TypeGrantTokens         = 55
	TypeRevokeTokens        = 56
	TypeChangeIssuer        = 70
	TypeEnableFreezing      = 71
	TypeDisableFreezing     = 72
	TypeFreezeTokens        = 185
	TypeUnfreezeTokens      = 186
	TypeAnyData             = 200
	TypeNonFungibleData     = 201
	TypeDeactivation        = 65533
	TypeActivation          = 65534
	TypeAlert               = 65535
)

// TypeName returns the label OmniLite uses for a transaction type
// (strTransactionType).
func TypeName(t uint16) string {
	switch t {
	case TypeSimpleSend:
		return "Simple Send"
	case TypeRestrictedSend:
		return "Restricted Send"
	case TypeSendToOwners:
		return "Send To Owners"
	case TypeSendAll:
		return "Send All"
	case TypeSendNonFungible:
		return "Unique Send"
	case TypeSavingsMark:
		return "Savings"
	case TypeSavingsCompromised:
		return "Savings COMPROMISED"
	case TypeRateLimitedMark:
		return "Rate-Limiting"
	case TypeAutomaticDispensary:
		return "Automatic Dispensary"
	case TypeTradeOffer:
		return "DEx Sell Offer"
	case TypeAcceptOffer:
		return "DEx Accept Offer"
	case TypeCreatePropertyFixed:
		return "Create Property - Fixed"
	case TypeCreatePropertyVar:
		return "Create Property - Variable"
	case TypePromoteProperty:
		return "Promote Property"
	case TypeCloseCrowdsale:
		return "Close Crowdsale"
	case TypeCreatePropertyMan:
		return "Create Property - Manual"
	case TypeGrantTokens:
		return "Grant Property Tokens"
	case TypeRevokeTokens:
		return "Revoke Property Tokens"
	case TypeChangeIssuer:
		return "Change Issuer Address"
	case TypeEnableFreezing:
		return "Enable Freezing"
	case TypeDisableFreezing:
		return "Disable Freezing"
	case TypeFreezeTokens:
		return "Freeze Property Tokens"
	case TypeUnfreezeTokens:
		return "Unfreeze Property Tokens"
	case TypeAnyData:
		return "Embed any data"
	case TypeNonFungibleData:
		return "Set Non-Fungible Token Data"
	case TypeNotification:
		return "Notification"
	case TypeAlert:
		return "ALERT"
	case TypeDeactivation:
		return "Feature Deactivation"
	case TypeActivation:
		return "Feature Activation"
	}
	return "* unknown type *"
}

// ErrInvalidPayload marks a payload that interpret_Transaction rejects.
// OmniLite does not record such transactions at all (PKT_ERROR-2).
var ErrInvalidPayload = errors.New("structurally invalid payload")

// Message is an interpreted payload. Only the fields of its type are set.
type Message struct {
	Version uint16
	Type    uint16
	// Receiver is the reference output, replaced by the sender for sends
	// to self and by the payload address for freeze/unfreeze.
	Receiver string

	Property             uint32
	Amount               uint64
	DistributionProperty uint32

	Ecosystem   uint8
	PropType    uint16
	PrevPropID  uint32
	Category    string
	Subcategory string
	Name        string
	URL         string
	Data        string
	Deadline    uint64
	EarlyBird   uint8
	Percentage  uint8

	AmountDesired  uint64
	BlockTimeLimit uint8
	MinFee         uint64
	Subaction      uint8

	TokenStart  uint64
	TokenEnd    uint64
	NFTDataType uint8
	NFTData     string

	AlertType   uint16
	AlertExpiry uint32
	AlertText   string

	FeatureID        uint16
	ActivationBlock  uint32
	MinClientVersion uint32
}

// stringFieldLen is SP_STRING_FIELD_LEN; strings are cut to one less.
const stringFieldLen = 256

// packet mimics the fixed, zero-padded pkt buffer of CMPTransaction:
// reads past the payload see zeros, and size is the real payload length.
type packet struct {
	buf  []byte
	size int
}

func newPacket(payload []byte) *packet {
	buf := make([]byte, 1+maxPayload)
	n := copy(buf, payload)
	return &packet{buf: buf, size: n}
}

func (p *packet) at(i, n int) []byte {
	out := make([]byte, n)
	if i < len(p.buf) {
		copy(out, p.buf[i:])
	}
	return out
}

func (p *packet) u8(i int) uint8   { return p.at(i, 1)[0] }
func (p *packet) u16(i int) uint16 { return binary.BigEndian.Uint16(p.at(i, 2)) }
func (p *packet) u32(i int) uint32 { return binary.BigEndian.Uint32(p.at(i, 4)) }
func (p *packet) u64(i int) uint64 { return binary.BigEndian.Uint64(p.at(i, 8)) }

// cstring reads a NUL-terminated string at i and returns it cut to the
// field length, plus the position after the terminator.
func (p *packet) cstring(i int) (string, int) {
	end := i
	for end < len(p.buf) && p.buf[end] != 0 {
		end++
	}
	s := []byte(nil)
	if i < len(p.buf) {
		s = p.buf[i:end]
	}
	next := i + len(s) + 1
	if len(s) > stringFieldLen-1 {
		s = s[:stringFieldLen-1]
	}
	return string(s), next
}

// overrun mirrors isOverrun: the read position is past the payload.
func (p *packet) overrun(pos int) bool { return pos > p.size }

// Interpret parses a payload like interpret_Transaction. sender and
// reference come from the parser; p is needed for addresses embedded in
// freeze and unfreeze payloads.
func Interpret(payload []byte, sender, reference string, params *chain.Params) (*Message, error) {
	pk := newPacket(payload)
	if pk.size < 4 {
		return nil, ErrInvalidPayload
	}
	m := &Message{Version: pk.u16(0), Type: pk.u16(2), Receiver: reference}
	ok := false
	switch m.Type {
	case TypeSimpleSend, TypeAcceptOffer, TypeRevokeTokens:
		if ok = pk.size >= 16; ok {
			m.Property, m.Amount = pk.u32(4), pk.u64(8)
			if m.Type == TypeSimpleSend && m.Receiver == "" {
				m.Receiver = sender
			}
		}
	case TypeSendToOwners:
		need := 20
		if m.Version == 0 {
			need = 16
		}
		if ok = pk.size >= need; ok {
			m.Property, m.Amount = pk.u32(4), pk.u64(8)
			if m.Version > 0 {
				m.DistributionProperty = pk.u32(16)
			}
		}
	case TypeSendAll:
		if ok = pk.size >= 5; ok {
			m.Ecosystem = pk.u8(4)
			m.Property = uint32(m.Ecosystem) // hint for the UI, as in OmniLite
			if m.Receiver == "" {
				m.Receiver = sender
			}
		}
	case TypeSendNonFungible:
		if ok = pk.size >= 24; ok {
			m.Property, m.TokenStart, m.TokenEnd = pk.u32(4), pk.u64(8), pk.u64(16)
		}
	case TypeTradeOffer:
		need := 34
		if m.Version == 0 {
			need = 33
		}
		if ok = pk.size >= need; ok {
			m.Property, m.Amount = pk.u32(4), pk.u64(8)
			m.AmountDesired, m.BlockTimeLimit, m.MinFee = pk.u64(16), pk.u8(24), pk.u64(25)
			if m.Version > 0 {
				m.Subaction = pk.u8(33)
			}
		}
	case TypeCreatePropertyFixed, TypeCreatePropertyVar, TypeCreatePropertyMan:
		ok = interpretCreate(pk, m)
	case TypeCloseCrowdsale, TypeChangeIssuer, TypeEnableFreezing, TypeDisableFreezing:
		if ok = pk.size >= 8; ok {
			m.Property = pk.u32(4)
		}
	case TypeGrantTokens:
		if ok = pk.size >= 16; ok {
			m.Property, m.Amount = pk.u32(4), pk.u64(8)
			m.NFTData, _ = pk.cstring(16)
			if m.Receiver == "" {
				m.Receiver = sender
			}
		}
	case TypeFreezeTokens, TypeUnfreezeTokens:
		if pk.size >= 37 {
			m.Property, m.Amount = pk.u32(4), pk.u64(8)
			// Virtual reference: the payload names the receiver.
			m.Receiver = hashToAddress(pk.u8(16), pk.at(17, 20), params)
			ok = m.Receiver != ""
		}
	case TypeAnyData:
		ok = true
	case TypeNonFungibleData:
		if ok = pk.size >= 25; ok {
			m.Property, m.TokenStart, m.TokenEnd = pk.u32(4), pk.u64(8), pk.u64(16)
			m.NFTDataType = pk.u8(24)
			m.NFTData, _ = pk.cstring(25)
		}
	case TypeDeactivation:
		if ok = pk.size >= 6; ok {
			m.FeatureID = pk.u16(4)
		}
	case TypeActivation:
		if ok = pk.size >= 14; ok {
			m.FeatureID, m.ActivationBlock, m.MinClientVersion = pk.u16(4), pk.u32(6), pk.u32(10)
		}
	case TypeAlert:
		if ok = pk.size >= 11; ok {
			m.AlertType, m.AlertExpiry = pk.u16(4), pk.u32(6)
			m.AlertText, _ = pk.cstring(10)
		}
	}
	if !ok {
		return nil, ErrInvalidPayload
	}
	return m, nil
}

// interpretCreate handles types 50, 51 and 54, which share the header and
// the five strings.
func interpretCreate(pk *packet, m *Message) bool {
	need := map[uint16]int{TypeCreatePropertyFixed: 25, TypeCreatePropertyVar: 39, TypeCreatePropertyMan: 17}[m.Type]
	if pk.size < need {
		return false
	}
	m.Ecosystem, m.PropType, m.PrevPropID = pk.u8(4), pk.u16(5), pk.u32(7)
	pos := 11
	fields := []*string{&m.Category, &m.Subcategory, &m.Name, &m.URL, &m.Data}
	for _, f := range fields {
		*f, pos = pk.cstring(pos)
	}
	switch m.Type {
	case TypeCreatePropertyFixed:
		m.Amount = pk.u64(pos)
		pos += 8
	case TypeCreatePropertyVar:
		m.Property = pk.u32(pos)
		m.Amount = pk.u64(pos + 4)
		m.Deadline = pk.u64(pos + 12)
		m.EarlyBird = pk.u8(pos + 20)
		m.Percentage = pk.u8(pos + 21)
		pos += 22
	}
	return !pk.overrun(pos)
}
