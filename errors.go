package ble

import "errors"

var (
	ErrUnavailable  = errors.New("ble unavailable")
	ErrUnauthorized = errors.New("ble unauthorized")
	ErrPoweredOff   = errors.New("ble powered off")
	ErrNotFound     = errors.New("ble peripheral not found")
	ErrDisconnected = errors.New("ble peripheral disconnected")
	ErrClosed       = errors.New("ble peripheral closed")
)

// Error preserves a native Bluetooth error while exposing a portable cause
// through errors.Is.
type Error struct {
	Operation   string
	Domain      string
	Code        int64
	Description string
	Err         error
}

func (e *Error) Error() string {
	if e.Description == "" {
		return e.Operation + " failed"
	}
	return e.Operation + ": " + e.Description
}

func (e *Error) Unwrap() error {
	return e.Err
}
