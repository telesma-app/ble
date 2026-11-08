# telesma-app/ble

[![Go Reference](https://pkg.go.dev/badge/github.com/telesma-app/ble.svg)](https://pkg.go.dev/github.com/telesma-app/ble)
[![Go](https://github.com/telesma-app/ble/actions/workflows/go.yml/badge.svg)](https://github.com/telesma-app/ble/actions/workflows/go.yml)

`telesma-app/ble` is an experimental, cgo-free Go library for Bluetooth LE
central/GATT clients on macOS. It calls CoreBluetooth through `purego/objc`.

> [!WARNING]
> This module is under active development. Its public API may change during `v0.x`.

## Features

The API intentionally covers discovery, connections, service and
characteristic discovery, acknowledged writes, reads, and notifications. It
does not manage pairing or bonds. When a remote characteristic requires link
security, the operating system may perform pairing and establish the encrypted
link as part of the GATT operation.

## Platform support

| Platform | Backend |
|---|---|
| macOS amd64/arm64 | CoreBluetooth and Foundation via `purego` |
| Linux and Windows | Unsupported stub returning `ble.ErrUnavailable` |

The backend supports macOS amd64 and arm64 without a C toolchain or a main
run loop. It is used by the experimental
[`ctap/backend/ble`](https://github.com/telesma-app/ctap/tree/master/backend/ble).

## Installation

```sh
go get github.com/telesma-app/ble@latest
```

See [`go.mod`](go.mod) for the required Go version.

## Quick start

Scan for FIDO advertisements for five seconds on macOS:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/telesma-app/ble"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for info, err := range ble.Scan(ctx, ble.UUID16(0xfffd)) {
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			log.Fatal(err)
		}
		fmt.Printf("%s %s %d dBm\n", info.ID, info.Name, info.RSSI)
	}
}
```

## Usage notes

`Scan` deduplicates by the opaque CoreBluetooth identifier and stops the
native scan when its context ends or its iterator consumer stops. An
advertisement is not a connection-state event, so this package deliberately
does not expose a `Watch` API.

`Open` can connect to an identifier discovered in the current process or one
known to CoreBluetooth. GATT operations on one peripheral are serialized. If
a context is canceled while a native operation is in flight, the Go call
returns promptly, but the next operation waits for the original CoreBluetooth
callback. A canceled connection remains busy until CoreBluetooth confirms its
disconnection. Writes are never retried automatically.

## Platform notes

On macOS, the host application is responsible for
[Bluetooth authorization](https://developer.apple.com/documentation/bundleresources/protected-resources)
and an appropriate `NSBluetoothAlwaysUsageDescription`. Command-line use also
requires Bluetooth permission for the terminal application. See Apple's
[`CBCentralManager` documentation](https://developer.apple.com/documentation/corebluetooth/cbcentralmanager)
for the native central-role lifecycle.

## Development

```sh
gofmt -l .
GOWORK=off go vet ./...
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off CGO_ENABLED=0 go test ./...
```

With golangci-lint v2 installed:

```sh
make lint
```

## License

[Apache License 2.0](LICENSE).
