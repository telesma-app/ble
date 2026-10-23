//go:build darwin

package ble

import (
	"bytes"
	"testing"
)

func TestNativeUUIDArrayPreservesOrder(t *testing.T) {
	if err := loadDarwinRuntime(); err != nil {
		t.Fatal(err)
	}

	if got := newCBUUIDArray(nil); got != 0 {
		t.Fatalf("empty UUID filter = %v, want nil native array", got)
	}

	want := []UUID{UUID16(0xfffd), UUID16(0x1800), UUID16(0xfffd)}

	objects := objectsFromNSArray(newCBUUIDArray(want))
	if len(objects) != len(want) {
		t.Fatalf("UUID count = %d, want %d", len(objects), len(want))
	}

	for i, object := range objects {
		got, err := uuidFromCBUUID(object)
		if err != nil {
			t.Fatal(err)
		}

		if got != want[i] {
			t.Errorf("UUID[%d] = %s, want %s", i, got, want[i])
		}
	}
}

func TestNativeDataOwnsValues(t *testing.T) {
	if err := loadDarwinRuntime(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		value []byte
	}{
		{name: "nil"},
		{name: "empty", value: []byte{}},
		{name: "value", value: []byte{1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := bytes.Clone(tt.value)

			native := newNSData(tt.value)
			if len(tt.value) != 0 {
				tt.value[0] ^= 0xff
			}

			got := dataFromNSData(native)
			if got == nil || !bytes.Equal(got, want) {
				t.Fatalf("native data = %#v, want non-nil %v", got, want)
			}

			if len(got) != 0 {
				got[0] ^= 0xff
			}

			if got := dataFromNSData(native); !bytes.Equal(got, want) {
				t.Fatalf("native data changed with returned bytes: %v, want %v", got, want)
			}
		})
	}
}
