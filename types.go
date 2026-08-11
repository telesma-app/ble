// Package ble provides the small Bluetooth Low Energy central/GATT client
// surface required by CTAP BLE transports.
package ble

import (
	"context"
	"io"
)

// Identifier is an opaque, platform-specific peripheral identifier.
type Identifier string

// DeviceInfo describes one peripheral advertisement.
type DeviceInfo struct {
	ID   Identifier
	Name string
	RSSI int
}

// CharacteristicProperties is the Bluetooth Characteristic Properties field.
type CharacteristicProperties uint16

const (
	// Bit 0 is the Bluetooth Broadcast property, which this client API does not expose.
	CharacteristicPropertyRead CharacteristicProperties = 1 << (iota + 1)
	CharacteristicPropertyWriteWithoutResponse
	CharacteristicPropertyWrite
	CharacteristicPropertyNotify
)

// Peripheral is an owned connection to a remote BLE peripheral.
type Peripheral interface {
	DiscoverServices(context.Context, ...UUID) ([]Service, error)
	io.Closer
}

// Service is one discovered remote GATT service.
type Service interface {
	UUID() UUID
	Primary() bool
	DiscoverCharacteristics(context.Context, ...UUID) ([]Characteristic, error)
}

// Characteristic is one discovered remote GATT characteristic. Write uses a
// GATT write request and waits for the peripheral's acknowledgement. Read is
// unavailable while the same characteristic has an active subscription.
type Characteristic interface {
	UUID() UUID
	Properties() CharacteristicProperties
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Subscribe(context.Context) (Subscription, error)
}

// Subscription publishes copied notification values in delivery order. If
// Listen closes unexpectedly, Close returns the terminal error.
type Subscription interface {
	Listen() <-chan []byte
	io.Closer
}
