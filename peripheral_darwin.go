//go:build darwin

package ble

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/ebitengine/purego/objc"
)

const cbCharacteristicWriteWithResponse = 0

type operationKind uint8

const (
	operationDiscoverServices operationKind = iota + 1
	operationDiscoverCharacteristics
	operationReadValueForCharacteristic
	operationWriteValueForCharacteristic
	operationEnableNotifications
	operationDisableNotifications
)

type operationResult struct {
	data []byte
	err  error
}

type darwinOperation struct {
	kind      operationKind
	target    objc.ID
	result    chan operationResult
	abandoned func(operationResult)
}

type darwinPeripheral struct {
	manager *darwinManager
	native  objc.ID
	gate    chan struct{}

	mu            sync.Mutex
	closed        bool
	closing       bool
	terminalErr   error
	pending       *darwinOperation
	subscriptions map[objc.ID]*darwinSubscription
	disconnected  chan struct{}

	closeOnce sync.Once
	closeErr  error
	doneOnce  sync.Once
}

func newDarwinPeripheral(manager *darwinManager, native objc.ID) *darwinPeripheral {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}

	return &darwinPeripheral{
		manager:       manager,
		native:        native,
		gate:          gate,
		subscriptions: make(map[objc.ID]*darwinSubscription),
		disconnected:  make(chan struct{}),
	}
}

func (p *darwinPeripheral) beginOperation(
	ctx context.Context,
	kind operationKind,
	target objc.ID,
	subscription *darwinSubscription,
) (*darwinOperation, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.gate:
	}

	op := &darwinOperation{
		kind:   kind,
		target: target,
		result: make(chan operationResult, 1),
	}

	p.mu.Lock()
	if p.closed || p.closing {
		err := p.terminalErr
		if err == nil {
			err = ErrClosed
		}
		p.mu.Unlock()

		p.gate <- struct{}{}

		return nil, err
	}

	if kind == operationReadValueForCharacteristic && p.subscriptions[target] != nil {
		p.mu.Unlock()

		p.gate <- struct{}{}

		return nil, errors.New("ble: cannot read a subscribed characteristic")
	}

	if subscription != nil {
		if p.subscriptions[target] != nil {
			p.mu.Unlock()

			p.gate <- struct{}{}

			return nil, errors.New("ble: characteristic already has a notification subscription")
		}

		p.subscriptions[target] = subscription
	}

	p.pending = op
	p.mu.Unlock()

	return op, nil
}

func (p *darwinPeripheral) runOperation(
	ctx context.Context,
	kind operationKind,
	target objc.ID,
	start func(),
) (operationResult, error) {
	op, err := p.beginOperation(ctx, kind, target, nil)
	if err != nil {
		return operationResult{}, err
	}

	start()

	result, err, _ := p.waitOperation(ctx, op, nil)

	return result, err
}

func (p *darwinPeripheral) waitOperation(
	ctx context.Context,
	op *darwinOperation,
	abandoned func(operationResult),
) (operationResult, error, bool) {
	select {
	case <-ctx.Done():
		p.mu.Lock()

		pending := p.pending == op
		if pending {
			op.abandoned = abandoned
		}
		p.mu.Unlock()

		if !pending {
			result := <-op.result
			return result, result.err, false
		}

		return operationResult{}, ctx.Err(), true
	case result := <-op.result:
		return result, result.err, false
	}
}

// The operation is detached from p.pending before delivery, so abandoned is final.
func (p *darwinPeripheral) deliverOperation(op *darwinOperation, result operationResult) {
	op.result <- result

	p.gate <- struct{}{}

	if op.abandoned != nil {
		go op.abandoned(result)
	}
}

func (p *darwinPeripheral) completeOperation(kind operationKind, target objc.ID, result operationResult) bool {
	p.mu.Lock()

	op := p.pending
	if op == nil || op.kind != kind || op.target != target {
		p.mu.Unlock()
		return false
	}

	p.pending = nil
	p.mu.Unlock()
	p.deliverOperation(op, result)

	return true
}

func (p *darwinPeripheral) completeNotificationOperation(target objc.ID, result operationResult) bool {
	p.mu.Lock()

	op := p.pending
	if op == nil || op.target != target || (op.kind != operationEnableNotifications && op.kind != operationDisableNotifications) {
		p.mu.Unlock()
		return false
	}

	if op.kind == operationDisableNotifications && result.err == nil {
		delete(p.subscriptions, target)
	}

	p.pending = nil
	p.mu.Unlock()
	p.deliverOperation(op, result)

	return true
}

func (p *darwinPeripheral) DiscoverServices(ctx context.Context, uuids ...UUID) ([]Service, error) {
	_, err := p.runOperation(ctx, operationDiscoverServices, p.native, func() {
		p.native.Send(selDiscoverServices, newCBUUIDArray(uuids))
	})
	if err != nil {
		return nil, err
	}

	var services []Service

	for _, native := range objectsFromNSArray(objc.Send[objc.ID](p.native, selServices)) {
		uuid, err := uuidFromCBUUID(objc.Send[objc.ID](native, selUUID))
		if err != nil {
			return nil, runtimeError("CBService.UUID", err)
		}

		if len(uuids) != 0 && !slices.Contains(uuids, uuid) {
			continue
		}

		services = append(services, &darwinService{
			peripheral: p,
			native:     native,
			uuid:       uuid,
			primary:    objc.Send[bool](native, selIsPrimary),
		})
	}

	return services, nil
}

func (p *darwinPeripheral) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		if p.closed {
			p.closeErr = p.terminalErr
			p.mu.Unlock()

			return
		}

		p.closing = true
		p.mu.Unlock()

		p.manager.central.Send(selCancelPeripheralConnection, p.native)

		if objc.Send[int](p.native, selState) != cbPeripheralStateConnected {
			p.handleDisconnect(nil)
		}

		<-p.disconnected
	})

	return p.closeErr
}

func (p *darwinPeripheral) handleDisconnect(err error) {
	p.doneOnce.Do(func() {
		p.mu.Lock()
		localClose := p.closing
		p.closed = true

		if !localClose {
			if err == nil {
				err = ErrDisconnected
			}

			p.terminalErr = err
			p.closeErr = err
		}

		pending := p.pending
		p.pending = nil
		operationErr := err
		subscriptionErr := err

		if localClose {
			operationErr = ErrClosed
			subscriptionErr = nil
		}

		subscriptions := p.subscriptions
		p.subscriptions = nil
		p.mu.Unlock()

		if pending != nil {
			p.deliverOperation(pending, operationResult{err: operationErr})
		}

		for _, subscription := range subscriptions {
			subscription.finish(subscriptionErr)
		}

		close(p.disconnected)
	})
}

func (p *darwinPeripheral) subscription(native objc.ID) *darwinSubscription {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.subscriptions[native]
}

func (p *darwinPeripheral) removeSubscription(subscription *darwinSubscription) {
	p.mu.Lock()
	if p.subscriptions[subscription.characteristic] == subscription {
		delete(p.subscriptions, subscription.characteristic)
	}
	p.mu.Unlock()
}

type darwinService struct {
	peripheral *darwinPeripheral
	native     objc.ID
	uuid       UUID
	primary    bool
}

func (s *darwinService) UUID() UUID {
	return s.uuid
}

func (s *darwinService) Primary() bool {
	return s.primary
}

func (s *darwinService) DiscoverCharacteristics(ctx context.Context, uuids ...UUID) ([]Characteristic, error) {
	_, err := s.peripheral.runOperation(ctx, operationDiscoverCharacteristics, s.native, func() {
		s.peripheral.native.Send(selDiscoverCharacteristicsForService, newCBUUIDArray(uuids), s.native)
	})
	if err != nil {
		return nil, err
	}

	var characteristics []Characteristic

	for _, native := range objectsFromNSArray(objc.Send[objc.ID](s.native, selCharacteristics)) {
		uuid, err := uuidFromCBUUID(objc.Send[objc.ID](native, selUUID))
		if err != nil {
			return nil, runtimeError("CBCharacteristic.UUID", err)
		}

		if len(uuids) != 0 && !slices.Contains(uuids, uuid) {
			continue
		}

		characteristics = append(characteristics, &darwinCharacteristic{
			service:    s,
			native:     native,
			uuid:       uuid,
			properties: CharacteristicProperties(objc.Send[uint16](native, selProperties)),
		})
	}

	return characteristics, nil
}

type darwinCharacteristic struct {
	service    *darwinService
	native     objc.ID
	uuid       UUID
	properties CharacteristicProperties
}

func (c *darwinCharacteristic) UUID() UUID {
	return c.uuid
}

func (c *darwinCharacteristic) Properties() CharacteristicProperties {
	return c.properties
}

func (c *darwinCharacteristic) Read(ctx context.Context) ([]byte, error) {
	result, err := c.service.peripheral.runOperation(ctx, operationReadValueForCharacteristic, c.native, func() {
		c.service.peripheral.native.Send(selReadValueForCharacteristic, c.native)
	})
	if err != nil {
		return nil, err
	}

	return result.data, nil
}

func (c *darwinCharacteristic) Write(ctx context.Context, value []byte) error {
	_, err := c.service.peripheral.runOperation(ctx, operationWriteValueForCharacteristic, c.native, func() {
		c.service.peripheral.native.Send(
			selWriteValueForCharacteristicType,
			newNSData(value),
			c.native,
			cbCharacteristicWriteWithResponse,
		)
	})

	return err
}

func (c *darwinCharacteristic) Subscribe(ctx context.Context) (Subscription, error) {
	p := c.service.peripheral
	subscription := &darwinSubscription{
		peripheral:     p,
		characteristic: c.native,
		values:         newValueQueue(),
	}

	op, err := p.beginOperation(ctx, operationEnableNotifications, c.native, subscription)
	if err != nil {
		subscription.values.Close()
		return nil, err
	}

	p.native.Send(selSetNotifyValueForCharacteristic, true, c.native)

	_, err, abandoned := p.waitOperation(ctx, op, subscription.cancelLateEnable)
	if err != nil {
		subscription.finish(err)

		if !abandoned {
			p.removeSubscription(subscription)
		}

		return nil, err
	}

	return subscription, nil
}

type darwinSubscription struct {
	peripheral     *darwinPeripheral
	characteristic objc.ID
	values         *valueQueue

	mu       sync.Mutex
	finished bool
	terminal error

	closeOnce sync.Once
	closeErr  error
}

func (s *darwinSubscription) Listen() <-chan []byte {
	return s.values.Listen()
}

func (s *darwinSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		finished := s.finished
		terminal := s.terminal
		s.mu.Unlock()

		if finished {
			s.closeErr = terminal
			return
		}

		_, err := s.peripheral.runOperation(context.Background(), operationDisableNotifications, s.characteristic, func() {
			s.peripheral.native.Send(selSetNotifyValueForCharacteristic, false, s.characteristic)
		})
		s.finish(err)
		s.closeErr = err
	})

	return s.closeErr
}

func (s *darwinSubscription) cancelLateEnable(result operationResult) {
	if result.err != nil {
		s.peripheral.removeSubscription(s)
		return
	}

	_, _ = s.peripheral.runOperation(context.Background(), operationDisableNotifications, s.characteristic, func() {
		s.peripheral.native.Send(selSetNotifyValueForCharacteristic, false, s.characteristic)
	})
}

func (s *darwinSubscription) finish(err error) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}

	s.finished = true
	s.terminal = err
	s.mu.Unlock()
	s.values.Close()
}

func managerPeripheral(self objc.ID, peripheral objc.ID) *darwinPeripheral {
	value, ok := delegateManagers.Load(self)
	if !ok {
		return nil
	}

	m := value.(*darwinManager)
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.active[peripheral]
}

func peripheralDidDiscoverServices(self objc.ID, _cmd objc.SEL, peripheral objc.ID, nsError objc.ID) {
	p := managerPeripheral(self, peripheral)
	if p != nil {
		p.completeOperation(
			operationDiscoverServices,
			peripheral,
			operationResult{err: errorFromNSError("discoverServices:", nsError, nil)},
		)
	}
}

func peripheralDidDiscoverCharacteristicsForService(
	self objc.ID,
	_cmd objc.SEL,
	peripheral objc.ID,
	service objc.ID,
	nsError objc.ID,
) {
	p := managerPeripheral(self, peripheral)
	if p != nil {
		p.completeOperation(
			operationDiscoverCharacteristics,
			service,
			operationResult{err: errorFromNSError("discoverCharacteristics:forService:", nsError, nil)},
		)
	}
}

func peripheralDidUpdateValueForCharacteristic(
	self objc.ID,
	_cmd objc.SEL,
	peripheral objc.ID,
	characteristic objc.ID,
	nsError objc.ID,
) {
	p := managerPeripheral(self, peripheral)
	if p == nil {
		return
	}

	err := errorFromNSError("readValueForCharacteristic:", nsError, nil)

	var data []byte
	if err == nil {
		data = dataFromNSData(objc.Send[objc.ID](characteristic, selValue))
	}

	if p.completeOperation(operationReadValueForCharacteristic, characteristic, operationResult{data: data, err: err}) {
		return
	}

	subscription := p.subscription(characteristic)
	if subscription == nil {
		return
	}

	if err != nil {
		subscription.finish(err)
		return
	}

	subscription.values.Send(data)
}

func peripheralDidWriteValueForCharacteristic(
	self objc.ID,
	_cmd objc.SEL,
	peripheral objc.ID,
	characteristic objc.ID,
	nsError objc.ID,
) {
	p := managerPeripheral(self, peripheral)
	if p != nil {
		p.completeOperation(
			operationWriteValueForCharacteristic,
			characteristic,
			operationResult{err: errorFromNSError("writeValue:forCharacteristic:type:", nsError, nil)},
		)
	}
}

func peripheralDidUpdateNotificationStateForCharacteristic(
	self objc.ID,
	_cmd objc.SEL,
	peripheral objc.ID,
	characteristic objc.ID,
	nsError objc.ID,
) {
	p := managerPeripheral(self, peripheral)
	if p == nil {
		return
	}

	err := errorFromNSError("setNotifyValue:forCharacteristic:", nsError, nil)
	if err == nil {
		p.mu.Lock()
		op := p.pending
		p.mu.Unlock()

		if op != nil && op.kind == operationEnableNotifications && !objc.Send[bool](characteristic, selIsNotifying) {
			err = errors.New("ble: peripheral did not enable notifications")
		}
	}

	p.completeNotificationOperation(characteristic, operationResult{err: err})
}
