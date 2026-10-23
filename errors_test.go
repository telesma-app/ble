package ble

import (
	"errors"
	"testing"
)

func TestNativeErrorPreservesMetadataAndCause(t *testing.T) {
	err := &Error{
		Operation:   "connectPeripheral:options:",
		Domain:      "CBErrorDomain",
		Code:        6,
		Description: "connection timeout",
		Err:         ErrDisconnected,
	}
	if !errors.Is(err, ErrDisconnected) {
		t.Fatal("native error does not unwrap to ErrDisconnected")
	}

	if err.Operation != "connectPeripheral:options:" || err.Domain != "CBErrorDomain" || err.Code != 6 {
		t.Fatalf("metadata = %#v", err)
	}
}
