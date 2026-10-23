//go:build darwin

package ble

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/ebitengine/purego/objc"
)

const (
	cbManagerStateUnknown = iota
	cbManagerStateResetting
	cbManagerStateUnsupported
	cbManagerStateUnauthorized
	cbManagerStatePoweredOff
	cbManagerStatePoweredOn
)

const cbPeripheralStateConnected = 2

var defaultManager = sync.OnceValues(newDarwinManager)

type darwinManager struct {
	central  objc.ID
	delegate objc.ID

	scanGate     chan struct{}
	stateChanged chan struct{}

	mu          sync.Mutex
	state       int
	scan        *darwinScan
	peripherals map[Identifier]objc.ID
	connects    map[objc.ID]chan error
	active      map[objc.ID]*darwinPeripheral
}

func newDarwinManager() (*darwinManager, error) {
	class, err := delegateClass()
	if err != nil {
		return nil, err
	}

	m := &darwinManager{
		scanGate:     make(chan struct{}, 1),
		stateChanged: make(chan struct{}),
		state:        cbManagerStateUnknown,
		peripherals:  make(map[Identifier]objc.ID),
		connects:     make(map[objc.ID]chan error),
		active:       make(map[objc.ID]*darwinPeripheral),
	}
	m.scanGate <- struct{}{}

	m.delegate = objc.Send[objc.ID](objc.ID(class), selNew)
	if m.delegate == 0 {
		return nil, invalidNativeValue("+[TelesmaBLEDelegate new]")
	}

	delegateManagers.Store(m.delegate, m)

	queue := dispatchQueueCreate("com.telesma.ble.central", 0)
	if queue == 0 {
		delegateManagers.Delete(m.delegate)
		m.delegate.Send(selRelease)

		return nil, invalidNativeValue("dispatch_queue_create")
	}

	allocated := objc.Send[objc.ID](objc.ID(objc.GetClass("CBCentralManager")), selAlloc)

	m.central = objc.Send[objc.ID](allocated, selInitWithDelegateQueueOptions, m.delegate, queue, objc.ID(0))
	if m.central == 0 {
		delegateManagers.Delete(m.delegate)
		m.delegate.Send(selRelease)

		return nil, invalidNativeValue("-[CBCentralManager initWithDelegate:queue:options:]")
	}

	m.mu.Lock()
	m.state = objc.Send[int](m.central, selState)
	m.mu.Unlock()

	return m, nil
}

func (m *darwinManager) waitReady(ctx context.Context) error {
	for {
		m.mu.Lock()
		state := m.state
		changed := m.stateChanged
		m.mu.Unlock()

		switch state {
		case cbManagerStatePoweredOn:
			return nil
		case cbManagerStateUnsupported, cbManagerStateUnauthorized, cbManagerStatePoweredOff:
			return managerStateError(state)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func managerStateError(state int) error {
	switch state {
	case cbManagerStateUnsupported:
		return errors.Join(ErrUnavailable, errors.New("bluetooth LE is unsupported"))
	case cbManagerStateUnauthorized:
		return errors.Join(ErrUnavailable, ErrUnauthorized)
	case cbManagerStatePoweredOff:
		return errors.Join(ErrUnavailable, ErrPoweredOff)
	default:
		return ErrUnavailable
	}
}

func (m *darwinManager) updateState(state int) {
	m.mu.Lock()
	m.state = state
	scan := m.scan
	close(m.stateChanged)
	m.stateChanged = make(chan struct{})
	m.mu.Unlock()

	if scan != nil && state != cbManagerStatePoweredOn {
		scan.finish(managerStateError(state))
	}
}

// Scan discovers peripherals advertising service until ctx is canceled or the
// iterator consumer stops. Each identifier is yielded at most once.
func Scan(ctx context.Context, service UUID) iter.Seq2[*DeviceInfo, error] {
	return func(yield func(*DeviceInfo, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}

		manager, err := defaultManager()
		if err != nil {
			yield(nil, err)
			return
		}

		if err := manager.waitReady(ctx); err != nil {
			yield(nil, err)
			return
		}

		scan, err := manager.startScan(ctx, service)
		if err != nil {
			yield(nil, err)
			return
		}
		defer scan.stop()

		for {
			info, scanErr := scan.next(ctx)
			if scanErr != nil {
				yield(nil, scanErr)
				return
			}

			if !yield(info, nil) {
				return
			}
		}
	}
}

type darwinScan struct {
	manager *darwinManager

	mu      sync.Mutex
	pending []*DeviceInfo
	seen    map[Identifier]struct{}
	err     error
	closed  bool
	wake    chan struct{}

	stopOnce sync.Once
}

func (m *darwinManager) startScan(ctx context.Context, service UUID) (*darwinScan, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.scanGate:
	}

	scan := &darwinScan{
		manager: m,
		seen:    make(map[Identifier]struct{}),
		wake:    make(chan struct{}, 1),
	}
	m.mu.Lock()
	if m.state != cbManagerStatePoweredOn {
		err := managerStateError(m.state)
		m.mu.Unlock()

		m.scanGate <- struct{}{}

		return nil, err
	}

	m.scan = scan
	m.central.Send(selScanForPeripheralsWithServicesOptions, newCBUUIDArray([]UUID{service}), objc.ID(0))
	m.mu.Unlock()

	return scan, nil
}

func (s *darwinScan) enqueue(info *DeviceInfo) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	if _, exists := s.seen[info.ID]; exists {
		s.mu.Unlock()
		return
	}

	s.seen[info.ID] = struct{}{}
	s.pending = append(s.pending, info)
	s.mu.Unlock()
	s.notify()
}

func (s *darwinScan) finish(err error) {
	s.mu.Lock()
	if !s.closed && s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.notify()
}

func (s *darwinScan) next(ctx context.Context) (*DeviceInfo, error) {
	for {
		s.mu.Lock()
		if len(s.pending) != 0 {
			info := s.pending[0]
			s.pending[0] = nil
			s.pending = s.pending[1:]
			s.mu.Unlock()

			return info, nil
		}

		err := s.err
		closed := s.closed
		s.mu.Unlock()

		if err != nil {
			return nil, err
		}

		if closed {
			return nil, ErrClosed
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.wake:
		}
	}
}

func (s *darwinScan) stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.pending = nil
		s.mu.Unlock()

		s.manager.central.Send(selStopScan)
		s.manager.mu.Lock()
		if s.manager.scan == s {
			s.manager.scan = nil
		}
		s.manager.mu.Unlock()

		s.manager.scanGate <- struct{}{}

		s.notify()
	})
}

func (s *darwinScan) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Open connects to a previously discovered or CoreBluetooth-known peripheral.
func Open(ctx context.Context, id Identifier) (Peripheral, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	manager, err := defaultManager()
	if err != nil {
		return nil, err
	}

	if err := manager.waitReady(ctx); err != nil {
		return nil, err
	}

	return manager.open(ctx, id)
}

func (m *darwinManager) open(ctx context.Context, id Identifier) (*darwinPeripheral, error) {
	m.mu.Lock()
	if m.state != cbManagerStatePoweredOn {
		err := managerStateError(m.state)
		m.mu.Unlock()

		return nil, err
	}

	native := m.peripherals[id]
	if native == 0 {
		native = m.retrievePeripheralLocked(id)
	}

	if native == 0 {
		m.mu.Unlock()
		return nil, ErrNotFound
	}

	if _, exists := m.active[native]; exists {
		m.mu.Unlock()
		return nil, errors.New("ble: peripheral is already open")
	}

	p := newDarwinPeripheral(m, native)
	m.active[native] = p

	native.Send(selSetDelegate, m.delegate)

	if objc.Send[int](native, selState) == cbPeripheralStateConnected {
		m.mu.Unlock()
		return p, nil
	}

	result := make(chan error, 1)
	m.connects[native] = result
	m.central.Send(selConnectPeripheralOptions, native, objc.ID(0))
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		m.central.Send(selCancelPeripheralConnection, native)
		return nil, ctx.Err()
	case err := <-result:
		if err != nil {
			m.removeActive(p)
			return nil, err
		}

		return p, nil
	}
}

// retrievePeripheralLocked runs while m.mu is held so a manager state update
// cannot interleave with the CoreBluetooth call.
func (m *darwinManager) retrievePeripheralLocked(id Identifier) objc.ID {
	identifier := objc.Send[objc.ID](objc.ID(objc.GetClass("NSUUID")), selAlloc)

	identifier = objc.Send[objc.ID](identifier, selInitWithUUIDString, newNSString(string(id)))
	if identifier == 0 {
		return 0
	}
	defer identifier.Send(selRelease)

	identifiers := objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableArray")), selArray)
	identifiers.Send(selAddObject, identifier)

	values := objectsFromNSArray(objc.Send[objc.ID](m.central, selRetrievePeripheralsWithIdentifiers, identifiers))
	if len(values) == 0 {
		return 0
	}

	native := values[0].Send(selRetain)
	m.peripherals[id] = native

	return native
}

func (m *darwinManager) rememberPeripheral(native objc.ID) {
	id := identifierFromCBPeripheral(native)

	m.mu.Lock()
	if _, exists := m.peripherals[id]; !exists {
		m.peripherals[id] = native.Send(selRetain)
	}
	m.mu.Unlock()
}

func (m *darwinManager) removeActive(p *darwinPeripheral) {
	m.mu.Lock()
	if m.active[p.native] == p {
		delete(m.active, p.native)
	}

	delete(m.connects, p.native)
	m.mu.Unlock()
}

func (m *darwinManager) completeConnect(native objc.ID, err error) {
	m.mu.Lock()
	result := m.connects[native]
	delete(m.connects, native)
	m.mu.Unlock()

	if result != nil {
		result <- err
	}
}

func centralManagerDidUpdateState(self objc.ID, _cmd objc.SEL, central objc.ID) {
	value, ok := delegateManagers.Load(self)
	if !ok {
		return
	}

	value.(*darwinManager).updateState(objc.Send[int](central, selState))
}

func centralManagerDidDiscoverPeripheral(
	self objc.ID,
	_cmd objc.SEL,
	central objc.ID,
	peripheral objc.ID,
	advertisementData objc.ID,
	RSSI objc.ID,
) {
	value, ok := delegateManagers.Load(self)
	if !ok {
		return
	}

	m := value.(*darwinManager)
	m.rememberPeripheral(peripheral)

	m.mu.Lock()
	scan := m.scan
	m.mu.Unlock()

	if scan == nil {
		return
	}

	scan.enqueue(&DeviceInfo{
		ID:   identifierFromCBPeripheral(peripheral),
		Name: stringFromNSString(objc.Send[objc.ID](peripheral, selName)),
		RSSI: objc.Send[int](RSSI, selIntegerValue),
	})
}

func centralManagerDidConnectPeripheral(self objc.ID, _cmd objc.SEL, central objc.ID, peripheral objc.ID) {
	value, ok := delegateManagers.Load(self)
	if ok {
		value.(*darwinManager).completeConnect(peripheral, nil)
	}
}

func centralManagerDidFailToConnectPeripheral(
	self objc.ID,
	_cmd objc.SEL,
	central objc.ID,
	peripheral objc.ID,
	nsError objc.ID,
) {
	value, ok := delegateManagers.Load(self)
	if ok {
		value.(*darwinManager).completeConnect(
			peripheral,
			errorFromNSError("connectPeripheral:options:", nsError, ErrDisconnected),
		)
	}
}

func centralManagerDidDisconnectPeripheral(
	self objc.ID,
	_cmd objc.SEL,
	central objc.ID,
	peripheral objc.ID,
	nsError objc.ID,
) {
	value, ok := delegateManagers.Load(self)
	if !ok {
		return
	}

	m := value.(*darwinManager)
	err := errorFromNSError("centralManager:didDisconnectPeripheral:error:", nsError, ErrDisconnected)
	m.completeConnect(peripheral, err)

	m.mu.Lock()
	p := m.active[peripheral]
	m.mu.Unlock()

	if p != nil {
		m.removeActive(p)
		p.handleDisconnect(err)
	}
}
