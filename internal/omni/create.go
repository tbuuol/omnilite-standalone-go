package omni

import "encoding/binary"

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
