//go:build !darwin

package ble

import (
	"context"
	"iter"
)

// Scan reports that the current platform backend is not implemented.
func Scan(ctx context.Context, _ UUID) iter.Seq2[*DeviceInfo, error] {
	return func(yield func(*DeviceInfo, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}

		yield(nil, ErrUnavailable)
	}
}

// Open reports that the current platform backend is not implemented.
func Open(ctx context.Context, _ Identifier) (Peripheral, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return nil, ErrUnavailable
}
