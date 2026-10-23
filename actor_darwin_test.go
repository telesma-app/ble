//go:build darwin

package ble

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
)

func newTestManager(state int) *darwinManager {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}

	return &darwinManager{
		scanGate:     gate,
		stateChanged: make(chan struct{}),
		state:        state,
		peripherals:  make(map[Identifier]objc.ID),
		connects:     make(map[objc.ID]chan error),
		active:       make(map[objc.ID]*darwinPeripheral),
	}
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()

	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for test event")

		var zero T

		return zero
	}
}

func TestWaitReadyAdapterErrors(t *testing.T) {
	tests := []struct {
		state int
		want  error
	}{
		{cbManagerStateUnsupported, ErrUnavailable},
		{cbManagerStateUnauthorized, ErrUnauthorized},
		{cbManagerStatePoweredOff, ErrPoweredOff},
	}
	for _, tt := range tests {
		manager := newTestManager(tt.state)
		if err := manager.waitReady(context.Background()); !errors.Is(err, tt.want) {
			t.Fatalf("state %d: error = %v, want %v", tt.state, err, tt.want)
		}
	}
}

func TestStateUpdateWakesEveryWaiter(t *testing.T) {
	manager := newTestManager(cbManagerStateUnknown)

	const waiters = 4

	var wait sync.WaitGroup

	errors := make(chan error, waiters)
	for range waiters {
		wait.Add(1)
		go func() {
			defer wait.Done()

			errors <- manager.waitReady(context.Background())
		}()
	}

	manager.updateState(cbManagerStatePoweredOn)

	done := make(chan struct{})

	go func() {
		wait.Wait()
		close(done)
	}()

	receive(t, done)
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanDeduplicatesAndStops(t *testing.T) {
	manager := newTestManager(cbManagerStatePoweredOn)
	<-manager.scanGate
	scan := &darwinScan{manager: manager, seen: make(map[Identifier]struct{}), wake: make(chan struct{}, 1)}
	manager.scan = scan

	scan.enqueue(&DeviceInfo{ID: "one", RSSI: -30})
	scan.enqueue(&DeviceInfo{ID: "one", RSSI: -40})

	info, err := scan.next(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if info.ID != "one" || info.RSSI != -30 {
		t.Fatalf("info = %#v", info)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := scan.next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("next error = %v, want context.Canceled", err)
	}

	scan.stop()
	manager.mu.Lock()
	active := manager.scan
	manager.mu.Unlock()

	if active != nil {
		t.Fatal("scan remained registered after stop")
	}

	select {
	case <-manager.scanGate:
	default:
		t.Fatal("scan gate was not released")
	}
}

func TestStartScanRejectsStateChangeAndReleasesGate(t *testing.T) {
	manager := newTestManager(cbManagerStatePoweredOff)
	if _, err := manager.startScan(context.Background(), UUID16(0xfffd)); !errors.Is(err, ErrPoweredOff) {
		t.Fatalf("startScan error = %v, want ErrPoweredOff", err)
	}

	select {
	case <-manager.scanGate:
	default:
		t.Fatal("scan gate was not released")
	}
}

func TestGATTOperationCancellationPreservesSerialization(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))
	firstStarted := make(chan struct{})
	firstDone := make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := peripheral.runOperation(ctx, operationReadValueForCharacteristic, objc.ID(2), func() { close(firstStarted) })
		firstDone <- err
	}()

	receive(t, firstStarted)
	cancel()

	if err := receive(t, firstDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("first error = %v", err)
	}

	if len(peripheral.gate) != 0 {
		t.Fatal("canceled operation released gate before its callback")
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)

	go func() {
		_, err := peripheral.runOperation(context.Background(), operationWriteValueForCharacteristic, objc.ID(3), func() { close(secondStarted) })
		secondDone <- err
	}()

	if !peripheral.completeOperation(operationReadValueForCharacteristic, objc.ID(2), operationResult{}) {
		t.Fatal("first callback was not routed")
	}

	receive(t, secondStarted)

	if !peripheral.completeOperation(operationWriteValueForCharacteristic, objc.ID(3), operationResult{}) {
		t.Fatal("second callback was not routed")
	}

	if err := receive(t, secondDone); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateCallbackRoutesByPeripheral(t *testing.T) {
	manager := newTestManager(cbManagerStatePoweredOn)
	delegate := objc.ID(0x1001)
	nativePeripheral := objc.ID(0x1002)
	peripheral := newDarwinPeripheral(manager, nativePeripheral)
	manager.active[nativePeripheral] = peripheral
	delegateManagers.Store(delegate, manager)
	t.Cleanup(func() { delegateManagers.Delete(delegate) })

	started := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := peripheral.runOperation(context.Background(), operationDiscoverServices, nativePeripheral, func() { close(started) })
		done <- err
	}()

	receive(t, started)
	peripheralDidDiscoverServices(delegate, 0, nativePeripheral, 0)

	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestDisconnectCompletesPendingOperationAndClosesOnce(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))
	started := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := peripheral.runOperation(context.Background(), operationReadValueForCharacteristic, objc.ID(2), func() { close(started) })
		done <- err
	}()

	receive(t, started)

	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()

			peripheral.handleDisconnect(ErrDisconnected)
		}()
	}

	disconnected := make(chan struct{})

	go func() {
		wait.Wait()
		close(disconnected)
	}()

	receive(t, disconnected)

	if err := receive(t, done); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("operation error = %v, want ErrDisconnected", err)
	}

	started = make(chan struct{})

	_, err := peripheral.runOperation(context.Background(), operationReadValueForCharacteristic, objc.ID(2), func() {
		close(started)
	})
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("future operation error = %v, want ErrDisconnected", err)
	}

	select {
	case <-started:
		t.Fatal("operation started after disconnect")
	default:
	}
}

func TestLocalDisconnectFinishesSubscriptionWithoutError(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))
	subscription := &darwinSubscription{
		peripheral:     peripheral,
		characteristic: objc.ID(2),
		values:         newValueQueue(),
	}
	peripheral.subscriptions[subscription.characteristic] = subscription
	peripheral.closing = true

	peripheral.handleDisconnect(ErrDisconnected)

	if _, ok := <-subscription.Listen(); ok {
		t.Fatal("subscription remained open")
	}

	if err := subscription.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}
}

func TestClosedPeripheralRejectsSubscribe(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))
	peripheral.handleDisconnect(ErrDisconnected)
	characteristic := &darwinCharacteristic{
		service: &darwinService{peripheral: peripheral},
		native:  objc.ID(2),
	}

	subscription, err := characteristic.Subscribe(context.Background())
	if subscription != nil {
		t.Fatal("Subscribe returned a subscription after disconnect")
	}

	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("Subscribe error = %v, want ErrDisconnected", err)
	}
}

func TestReadRejectsSubscribedCharacteristic(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))
	subscription := &darwinSubscription{
		peripheral:     peripheral,
		characteristic: objc.ID(2),
		values:         newValueQueue(),
	}
	t.Cleanup(subscription.values.Close)
	peripheral.subscriptions[subscription.characteristic] = subscription
	characteristic := &darwinCharacteristic{
		service: &darwinService{peripheral: peripheral},
		native:  subscription.characteristic,
	}

	if _, err := characteristic.Read(context.Background()); err == nil {
		t.Fatal("Read succeeded while the characteristic was subscribed")
	}
}

func TestDisableCompletionRemovesSubscription(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))
	subscription := &darwinSubscription{
		peripheral:     peripheral,
		characteristic: objc.ID(2),
		values:         newValueQueue(),
	}
	t.Cleanup(subscription.values.Close)
	peripheral.subscriptions[subscription.characteristic] = subscription

	op, err := peripheral.beginOperation(
		context.Background(),
		operationDisableNotifications,
		subscription.characteristic,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if !peripheral.completeNotificationOperation(op.target, operationResult{}) {
		t.Fatal("disable callback was not routed")
	}

	if _, err, _ := peripheral.waitOperation(context.Background(), op, nil); err != nil {
		t.Fatal(err)
	}

	if peripheral.subscription(subscription.characteristic) != nil {
		t.Fatal("subscription remained registered after disable")
	}
}

func TestCanceledOperationRunsLateCleanup(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))

	op, err := peripheral.beginOperation(
		context.Background(),
		operationEnableNotifications,
		objc.ID(2),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanup := make(chan operationResult, 1)

	_, err, abandoned := peripheral.waitOperation(ctx, op, func(result operationResult) {
		cleanup <- result
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitOperation error = %v, want context.Canceled", err)
	}

	if !abandoned {
		t.Fatal("canceled operation was not abandoned")
	}

	want := operationResult{data: []byte{1}}
	if !peripheral.completeNotificationOperation(op.target, want) {
		t.Fatal("late callback was not routed")
	}

	got := receive(t, cleanup)
	if len(got.data) != 1 || got.data[0] != 1 || got.err != nil {
		t.Fatalf("cleanup result = %#v, want %#v", got, want)
	}
}

func TestCompletedOperationWinsCancellationRace(t *testing.T) {
	peripheral := newDarwinPeripheral(nil, objc.ID(1))

	op, err := peripheral.beginOperation(
		context.Background(),
		operationEnableNotifications,
		objc.ID(2),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := operationResult{data: []byte{1}}
	if !peripheral.completeNotificationOperation(op.target, want) {
		t.Fatal("callback was not routed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanup := make(chan struct{}, 1)

	got, err, abandoned := peripheral.waitOperation(ctx, op, func(operationResult) {
		cleanup <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}

	if abandoned {
		t.Fatal("completed operation was abandoned")
	}

	if len(got.data) != 1 || got.data[0] != 1 {
		t.Fatalf("result = %#v, want %#v", got, want)
	}

	select {
	case <-cleanup:
		t.Fatal("completed operation ran abandoned cleanup")
	default:
	}
}

func TestOpenRejectsStateChange(t *testing.T) {
	manager := newTestManager(cbManagerStatePoweredOff)
	if _, err := manager.open(context.Background(), "peripheral"); !errors.Is(err, ErrPoweredOff) {
		t.Fatalf("open error = %v, want ErrPoweredOff", err)
	}
}

func TestDisconnectRemovesPeripheralBeforePublishingCompletion(t *testing.T) {
	manager := newTestManager(cbManagerStatePoweredOn)
	delegate := objc.ID(0x2001)
	native := objc.ID(0x2002)
	peripheral := newDarwinPeripheral(manager, native)
	connect := make(chan error, 1)
	manager.active[native] = peripheral
	manager.connects[native] = connect
	delegateManagers.Store(delegate, manager)
	t.Cleanup(func() { delegateManagers.Delete(delegate) })

	centralManagerDidDisconnectPeripheral(delegate, 0, 0, native, 0)

	if err := receive(t, connect); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("connect error = %v, want ErrDisconnected", err)
	}

	<-peripheral.disconnected
	manager.mu.Lock()
	active := manager.active[native]
	manager.mu.Unlock()

	if active != nil {
		t.Fatal("peripheral remained active after disconnect completion")
	}
}
