package ble

import "testing"

func TestUUID16(t *testing.T) {
	uuid := UUID16(0xfffd)
	if got, want := uuid.String(), "0000fffd-0000-1000-8000-00805f9b34fb"; got != want {
		t.Fatalf("UUID16(0xfffd) = %q, want %q", got, want)
	}
}

func TestParseUUID(t *testing.T) {
	tests := []string{
		"fffd",
		"0000fffd",
		"0000fffd-0000-1000-8000-00805f9b34fb",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			uuid, err := parseUUID(value)
			if err != nil {
				t.Fatal(err)
			}

			if uuid != UUID16(0xfffd) {
				t.Fatalf("parseUUID(%q) = %s, want %s", value, uuid, UUID16(0xfffd))
			}
		})
	}
}
