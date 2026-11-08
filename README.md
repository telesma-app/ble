# telesma-app/ble

`github.com/telesma-app/ble` is a small, cgo-free BLE Central/GATT client for
Go. Its first backend calls the Objective-C CoreBluetooth API through
`purego/objc`; no TinyGo, C compiler, Objective-C shim, or main run loop is
required.

The API intentionally covers discovery, connections, service and
characteristic discovery, acknowledged writes, reads, and notifications. It
does not manage pairing or bonds. When a remote characteristic requires link
security, the operating system may perform pairing and establish the encrypted
link as part of the GATT operation.

## Platforms

| Platform | Backend |
|---|---|
| macOS amd64/arm64 | CoreBluetooth and Foundation via `purego` |
| Linux and Windows | Unsupported stub returning `ble.ErrUnavailable` |

The module requires Go 1.27.3 or newer. It is currently an untagged prototype
used by the CTAP BLE backend in `github.com/telesma-app/ctap`.

## Usage

```go
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
```

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

On macOS, the host application is responsible for
[Bluetooth authorization](https://developer.apple.com/documentation/bundleresources/protected-resources)
and an appropriate `NSBluetoothAlwaysUsageDescription`. Command-line use also
requires Bluetooth permission for the terminal application. See Apple's
[`CBCentralManager` documentation](https://developer.apple.com/documentation/corebluetooth/cbcentralmanager)
for the native central-role lifecycle.

## Linting

With golangci-lint v2 installed:

```sh
make lint
```

## Testing

```sh
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go test ./...
```
