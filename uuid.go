package ble

import (
	"encoding/hex"
	"fmt"
)

// UUID is a canonical 128-bit Bluetooth UUID in network byte order.
type UUID [16]byte

var bluetoothBaseUUID = UUID{
	0x00, 0x00, 0x00, 0x00,
	0x00, 0x00,
	0x10, 0x00,
	0x80, 0x00,
	0x00, 0x80, 0x5f, 0x9b, 0x34, 0xfb,
}

// UUID16 expands a 16-bit Bluetooth UUID into the Bluetooth base UUID.
func UUID16(value uint16) UUID {
	uuid := bluetoothBaseUUID
	uuid[2] = byte(value >> 8)
	uuid[3] = byte(value)
	return uuid
}

func (u UUID) String() string {
	encoded := hex.EncodeToString(u[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func parseUUID(value string) (UUID, error) {
	var uuid UUID
	switch len(value) {
	case 4:
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return UUID{}, err
		}
		return UUID16(uint16(decoded[0])<<8 | uint16(decoded[1])), nil
	case 8:
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return UUID{}, err
		}
		uuid = bluetoothBaseUUID
		copy(uuid[:4], decoded)
		return uuid, nil
	case 36:
		compact := value[0:8] + value[9:13] + value[14:18] + value[19:23] + value[24:36]
		decoded, err := hex.DecodeString(compact)
		if err != nil {
			return UUID{}, err
		}
		copy(uuid[:], decoded)
		return uuid, nil
	default:
		return UUID{}, fmt.Errorf("ble: invalid UUID %q", value)
	}
}
