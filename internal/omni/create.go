package omni

import (
	"encoding/binary"

	"github.com/tbuuol/omnilite-standalone/internal/chain"
)

// Payload builders, ported from createpayload.cpp. All fields are big-endian.

func payloadHeader(version, typ uint16) []byte {
	p := make([]byte, 0, 32)
	p = binary.BigEndian.AppendUint16(p, version)
	return binary.BigEndian.AppendUint16(p, typ)
}

// PayloadSimpleSend mirrors CreatePayload_SimpleSend.
func PayloadSimpleSend(property uint32, amount uint64) []byte {
	p := payloadHeader(0, TypeSimpleSend)
	p = binary.BigEndian.AppendUint32(p, property)
	return binary.BigEndian.AppendUint64(p, amount)
}

// PayloadSendAll mirrors CreatePayload_SendAll.
func PayloadSendAll(ecosystem uint8) []byte {
	return append(payloadHeader(0, TypeSendAll), ecosystem)
}

// PayloadSendNonFungible mirrors CreatePayload_SendNonFungible.
func PayloadSendNonFungible(property uint32, start, end uint64) []byte {
	p := payloadHeader(0, TypeSendNonFungible)
	p = binary.BigEndian.AppendUint32(p, property)
	p = binary.BigEndian.AppendUint64(p, start)
	return binary.BigEndian.AppendUint64(p, end)
}

// PayloadAnyData mirrors CreatePayload_AnyData.
func PayloadAnyData(data []byte) []byte {
	return append(payloadHeader(0, TypeAnyData), data...)
}

// cString appends s, cut to 255 bytes, and a terminating zero.
func cString(p []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	return append(append(p, s...), 0)
}

// PropertyInfo holds the descriptive fields of a new property.
type PropertyInfo struct {
	Ecosystem   uint8
	Type        uint16
	PreviousID  uint32
	Category    string
	Subcategory string
	Name        string
	URL         string
	Data        string
}

func (i *PropertyInfo) appendTo(p []byte) []byte {
	p = append(p, i.Ecosystem)
	p = binary.BigEndian.AppendUint16(p, i.Type)
	p = binary.BigEndian.AppendUint32(p, i.PreviousID)
	for _, s := range []string{i.Category, i.Subcategory, i.Name, i.URL, i.Data} {
		p = cString(p, s)
	}
	return p
}

// PayloadIssuanceFixed mirrors CreatePayload_IssuanceFixed.
func PayloadIssuanceFixed(info *PropertyInfo, amount uint64) []byte {
	p := info.appendTo(payloadHeader(0, TypeCreatePropertyFixed))
	return binary.BigEndian.AppendUint64(p, amount)
}

// PayloadIssuanceCrowdsale mirrors CreatePayload_IssuanceVariable.
func PayloadIssuanceCrowdsale(info *PropertyInfo, desired uint32, perUnit, deadline uint64, earlyBonus, issuerPercentage uint8) []byte {
	p := info.appendTo(payloadHeader(0, TypeCreatePropertyVar))
	p = binary.BigEndian.AppendUint32(p, desired)
	p = binary.BigEndian.AppendUint64(p, perUnit)
	p = binary.BigEndian.AppendUint64(p, deadline)
	return append(p, earlyBonus, issuerPercentage)
}

// PayloadIssuanceManaged mirrors CreatePayload_IssuanceManaged.
func PayloadIssuanceManaged(info *PropertyInfo) []byte {
	return info.appendTo(payloadHeader(0, TypeCreatePropertyMan))
}

// PayloadSendToOwners mirrors CreatePayload_SendToOwners: version 1 when
// the owners of another property receive the tokens.
func PayloadSendToOwners(property uint32, amount uint64, distribution uint32) []byte {
	if property == distribution {
		p := binary.BigEndian.AppendUint32(payloadHeader(0, TypeSendToOwners), property)
		return binary.BigEndian.AppendUint64(p, amount)
	}
	p := binary.BigEndian.AppendUint32(payloadHeader(1, TypeSendToOwners), property)
	p = binary.BigEndian.AppendUint64(p, amount)
	return binary.BigEndian.AppendUint32(p, distribution)
}

// propertyOnly is the payload of types that carry just a property id.
func propertyOnly(typ uint16, property uint32) []byte {
	return binary.BigEndian.AppendUint32(payloadHeader(0, typ), property)
}

// PayloadCloseCrowdsale mirrors CreatePayload_CloseCrowdsale.
func PayloadCloseCrowdsale(property uint32) []byte {
	return propertyOnly(TypeCloseCrowdsale, property)
}

// PayloadChangeIssuer mirrors CreatePayload_ChangeIssuer.
func PayloadChangeIssuer(property uint32) []byte { return propertyOnly(TypeChangeIssuer, property) }

// PayloadEnableFreezing mirrors CreatePayload_EnableFreezing.
func PayloadEnableFreezing(property uint32) []byte {
	return propertyOnly(TypeEnableFreezing, property)
}

// PayloadDisableFreezing mirrors CreatePayload_DisableFreezing.
func PayloadDisableFreezing(property uint32) []byte {
	return propertyOnly(TypeDisableFreezing, property)
}

// PayloadGrant mirrors CreatePayload_Grant.
func PayloadGrant(property uint32, amount uint64, info string) []byte {
	p := binary.BigEndian.AppendUint32(payloadHeader(0, TypeGrantTokens), property)
	return cString(binary.BigEndian.AppendUint64(p, amount), info)
}

// PayloadRevoke mirrors CreatePayload_Revoke.
func PayloadRevoke(property uint32, amount uint64, memo string) []byte {
	p := binary.BigEndian.AppendUint32(payloadHeader(0, TypeRevokeTokens), property)
	return cString(binary.BigEndian.AppendUint64(p, amount), memo)
}

// PayloadFreeze mirrors CreatePayload_FreezeTokens (freeze) and
// CreatePayload_UnfreezeTokens. Unlike OmniLite, a P2SH address is written
// with the version byte 5 instead of 50: HashToAddress only knows 5, so
// the "M..." version byte would make the transaction invalid.
func PayloadFreeze(freeze bool, property uint32, amount uint64, target Destination, params *chain.Params) []byte {
	typ := uint16(TypeUnfreezeTokens)
	if freeze {
		typ = TypeFreezeTokens
	}
	p := binary.BigEndian.AppendUint32(payloadHeader(0, typ), property)
	p = binary.BigEndian.AppendUint64(p, amount)
	version := params.PubKeyPrefix
	if target.Kind == DestScriptHash {
		version = params.ScriptPrefix
	}
	return append(append(p, version), target.Hash[:]...)
}

// PayloadSetNonFungibleData mirrors CreatePayload_SetNonFungibleData.
func PayloadSetNonFungibleData(property uint32, start, end uint64, issuer bool, data string) []byte {
	p := binary.BigEndian.AppendUint32(payloadHeader(0, TypeNonFungibleData), property)
	p = binary.BigEndian.AppendUint64(p, start)
	p = binary.BigEndian.AppendUint64(p, end)
	flag := byte(0)
	if issuer {
		flag = 1
	}
	return cString(append(p, flag), data)
}
