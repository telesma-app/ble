//go:build darwin

package ble

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	selAlloc                                                 = objc.RegisterName("alloc")
	selNew                                                   = objc.RegisterName("new")
	selInitWithDelegateQueueOptions                          = objc.RegisterName("initWithDelegate:queue:options:")
	selState                                                 = objc.RegisterName("state")
	selScanForPeripheralsWithServicesOptions                 = objc.RegisterName("scanForPeripheralsWithServices:options:")
	selStopScan                                              = objc.RegisterName("stopScan")
	selConnectPeripheralOptions                              = objc.RegisterName("connectPeripheral:options:")
	selCancelPeripheralConnection                            = objc.RegisterName("cancelPeripheralConnection:")
	selRetrievePeripheralsWithIdentifiers                    = objc.RegisterName("retrievePeripheralsWithIdentifiers:")
	selIdentifier                                            = objc.RegisterName("identifier")
	selUUIDString                                            = objc.RegisterName("UUIDString")
	selName                                                  = objc.RegisterName("name")
	selIntegerValue                                          = objc.RegisterName("integerValue")
	selUTF8String                                            = objc.RegisterName("UTF8String")
	selStringWithUTF8String                                  = objc.RegisterName("stringWithUTF8String:")
	selUUIDWithString                                        = objc.RegisterName("UUIDWithString:")
	selInitWithUUIDString                                    = objc.RegisterName("initWithUUIDString:")
	selArray                                                 = objc.RegisterName("array")
	selAddObject                                             = objc.RegisterName("addObject:")
	selCount                                                 = objc.RegisterName("count")
	selObjectAtIndex                                         = objc.RegisterName("objectAtIndex:")
	selSetDelegate                                           = objc.RegisterName("setDelegate:")
	selDiscoverServices                                      = objc.RegisterName("discoverServices:")
	selServices                                              = objc.RegisterName("services")
	selUUID                                                  = objc.RegisterName("UUID")
	selIsPrimary                                             = objc.RegisterName("isPrimary")
	selDiscoverCharacteristicsForService                     = objc.RegisterName("discoverCharacteristics:forService:")
	selCharacteristics                                       = objc.RegisterName("characteristics")
	selProperties                                            = objc.RegisterName("properties")
	selReadValueForCharacteristic                            = objc.RegisterName("readValueForCharacteristic:")
	selWriteValueForCharacteristicType                       = objc.RegisterName("writeValue:forCharacteristic:type:")
	selSetNotifyValueForCharacteristic                       = objc.RegisterName("setNotifyValue:forCharacteristic:")
	selIsNotifying                                           = objc.RegisterName("isNotifying")
	selValue                                                 = objc.RegisterName("value")
	selDataWithBytesLength                                   = objc.RegisterName("dataWithBytes:length:")
	selLength                                                = objc.RegisterName("length")
	selBytes                                                 = objc.RegisterName("bytes")
	selDomain                                                = objc.RegisterName("domain")
	selCode                                                  = objc.RegisterName("code")
	selLocalizedDescription                                  = objc.RegisterName("localizedDescription")
	selRetain                                                = objc.RegisterName("retain")
	selRelease                                               = objc.RegisterName("release")
	selCentralManagerDidUpdateState                          = objc.RegisterName("centralManagerDidUpdateState:")
	selCentralManagerDidDiscoverPeripheral                   = objc.RegisterName("centralManager:didDiscoverPeripheral:advertisementData:RSSI:")
	selCentralManagerDidConnectPeripheral                    = objc.RegisterName("centralManager:didConnectPeripheral:")
	selCentralManagerDidFailToConnectPeripheral              = objc.RegisterName("centralManager:didFailToConnectPeripheral:error:")
	selCentralManagerDidDisconnectPeripheral                 = objc.RegisterName("centralManager:didDisconnectPeripheral:error:")
	selPeripheralDidDiscoverServices                         = objc.RegisterName("peripheral:didDiscoverServices:")
	selPeripheralDidDiscoverCharacteristicsForService        = objc.RegisterName("peripheral:didDiscoverCharacteristicsForService:error:")
	selPeripheralDidUpdateValueForCharacteristic             = objc.RegisterName("peripheral:didUpdateValueForCharacteristic:error:")
	selPeripheralDidWriteValueForCharacteristic              = objc.RegisterName("peripheral:didWriteValueForCharacteristic:error:")
	selPeripheralDidUpdateNotificationStateForCharacteristic = objc.RegisterName("peripheral:didUpdateNotificationStateForCharacteristic:error:")
)

var (
	dispatchQueueCreate func(string, uintptr) uintptr

	loadDarwinRuntime = sync.OnceValue(initDarwinRuntime)
	delegateClass     = sync.OnceValues(registerDelegateClass)
	delegateManagers  sync.Map
)

func initDarwinRuntime() error {
	// These libraries stay loaded for the process lifetime.
	if _, err := purego.Dlopen(
		"/System/Library/Frameworks/CoreBluetooth.framework/CoreBluetooth",
		purego.RTLD_NOW|purego.RTLD_GLOBAL,
	); err != nil {
		return errors.Join(ErrUnavailable, err)
	}

	if _, err := purego.Dlopen(
		"/System/Library/Frameworks/Foundation.framework/Foundation",
		purego.RTLD_NOW|purego.RTLD_GLOBAL,
	); err != nil {
		return errors.Join(ErrUnavailable, err)
	}

	libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}

	purego.RegisterLibFunc(&dispatchQueueCreate, libSystem, "dispatch_queue_create")

	return nil
}

func registerDelegateClass() (objc.Class, error) {
	if err := loadDarwinRuntime(); err != nil {
		return 0, err
	}

	return objc.RegisterClass(
		"TelesmaBLEDelegate",
		objc.GetClass("NSObject"),
		[]*objc.Protocol{
			objc.GetProtocol("CBCentralManagerDelegate"),
			objc.GetProtocol("CBPeripheralDelegate"),
		},
		nil,
		[]objc.MethodDef{
			{Cmd: selCentralManagerDidUpdateState, Fn: centralManagerDidUpdateState},
			{Cmd: selCentralManagerDidDiscoverPeripheral, Fn: centralManagerDidDiscoverPeripheral},
			{Cmd: selCentralManagerDidConnectPeripheral, Fn: centralManagerDidConnectPeripheral},
			{Cmd: selCentralManagerDidFailToConnectPeripheral, Fn: centralManagerDidFailToConnectPeripheral},
			{Cmd: selCentralManagerDidDisconnectPeripheral, Fn: centralManagerDidDisconnectPeripheral},
			{Cmd: selPeripheralDidDiscoverServices, Fn: peripheralDidDiscoverServices},
			{Cmd: selPeripheralDidDiscoverCharacteristicsForService, Fn: peripheralDidDiscoverCharacteristicsForService},
			{Cmd: selPeripheralDidUpdateValueForCharacteristic, Fn: peripheralDidUpdateValueForCharacteristic},
			{Cmd: selPeripheralDidWriteValueForCharacteristic, Fn: peripheralDidWriteValueForCharacteristic},
			{Cmd: selPeripheralDidUpdateNotificationStateForCharacteristic, Fn: peripheralDidUpdateNotificationStateForCharacteristic},
		},
	)
}

func newNSString(value string) objc.ID {
	return objc.Send[objc.ID](objc.ID(objc.GetClass("NSString")), selStringWithUTF8String, value)
}

func stringFromNSString(value objc.ID) string {
	if value == 0 {
		return ""
	}

	return objc.Send[string](value, selUTF8String)
}

func newCBUUID(uuid UUID) objc.ID {
	return objc.Send[objc.ID](objc.ID(objc.GetClass("CBUUID")), selUUIDWithString, newNSString(uuid.String()))
}

func newCBUUIDArray(values []UUID) objc.ID {
	if len(values) == 0 {
		return 0
	}

	array := objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableArray")), selArray)
	for _, value := range values {
		array.Send(selAddObject, newCBUUID(value))
	}

	return array
}

func uuidFromCBUUID(value objc.ID) (UUID, error) {
	text := stringFromNSString(objc.Send[objc.ID](value, selUUIDString))
	return parseUUID(text)
}

func objectsFromNSArray(value objc.ID) []objc.ID {
	if value == 0 {
		return nil
	}

	count := objc.Send[uintptr](value, selCount)

	result := make([]objc.ID, count)
	for index := range result {
		result[index] = objc.Send[objc.ID](value, selObjectAtIndex, uintptr(index))
	}

	return result
}

func newNSData(value []byte) objc.ID {
	pointer := unsafe.Pointer(unsafe.SliceData(value))
	data := objc.Send[objc.ID](objc.ID(objc.GetClass("NSData")), selDataWithBytesLength, pointer, uintptr(len(value)))
	runtime.KeepAlive(value)

	return data
}

func dataFromNSData(value objc.ID) []byte {
	if value == 0 {
		return nil
	}

	length := objc.Send[uintptr](value, selLength)
	if length == 0 {
		return []byte{}
	}

	pointer := objc.Send[unsafe.Pointer](value, selBytes)

	return append([]byte(nil), unsafe.Slice((*byte)(pointer), length)...)
}

func errorFromNSError(operation string, value objc.ID, cause error) error {
	if value == 0 {
		return cause
	}

	domain := stringFromNSString(objc.Send[objc.ID](value, selDomain))
	description := stringFromNSString(objc.Send[objc.ID](value, selLocalizedDescription))
	code := objc.Send[int64](value, selCode)

	return &Error{
		Operation:   operation,
		Domain:      domain,
		Code:        code,
		Description: description,
		Err:         cause,
	}
}

func identifierFromCBPeripheral(peripheral objc.ID) Identifier {
	identifier := objc.Send[objc.ID](peripheral, selIdentifier)
	return Identifier(stringFromNSString(objc.Send[objc.ID](identifier, selUUIDString)))
}

func runtimeError(operation string, err error) error {
	return &Error{Operation: operation, Description: err.Error(), Err: err}
}

func invalidNativeValue(operation string) error {
	return runtimeError(operation, errors.New("ble: native API returned an invalid value"))
}
