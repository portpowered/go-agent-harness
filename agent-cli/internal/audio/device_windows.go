//go:build windows

package audio

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	wasapiBackend = "wasapi"

	coinitMultithreaded = 0
	clsctxAll           = 0x17
	deviceStateActive   = 0x1
	roleConsole         = 0
	shareModeShared     = 0

	vtLPWSTR = 31

	hresultNotFound                           = 0x80070490
	audclntDeviceInvalidated                  = 0x88890004
	audclntDeviceInUse                        = 0x8889000a
	mmdeviceDataFlowRender                    = 0
	mmdeviceDataFlowCapture                   = 1
	immDeviceEnumeratorVTable                 = 3
	immDeviceCollectionVTable                 = 3
	immDeviceVTableGetID                      = 5
	immDeviceVTableActivate                   = 3
	immDeviceVTablePropertyStore              = 4
	propertyStoreVTableGetValue               = 5
	audioClientVTableInitialize               = 3
	audioClientVTableGetBufferSize            = 4
	audioClientVTableGetCurrentPadding        = 6
	audioClientVTableGetMixFormat             = 8
	audioClientVTableGetService               = 14
	audioClientVTableStart                    = 10
	audioClientVTableStop                     = 11
	audioCaptureClientVTableGetNextPacketSize = 5
	audioRenderClientVTableGetBuffer          = 3
	audioRenderClientVTableReleaseBuffer      = 4
	audclntBufferFlagsSilent                  = 0x2
)

var (
	wasapiOle32                   = syscall.NewLazyDLL("ole32.dll")
	wasapiCoInitializeEx          = wasapiOle32.NewProc("CoInitializeEx")
	wasapiCoUninitialize          = wasapiOle32.NewProc("CoUninitialize")
	wasapiCoCreateInstance        = wasapiOle32.NewProc("CoCreateInstance")
	wasapiCoTaskMemFree           = wasapiOle32.NewProc("CoTaskMemFree")
	wasapiPropVariantClear        = wasapiOle32.NewProc("PropVariantClear")
	wasapiCLSIDMMDeviceEnumerator = syscall.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
	wasapiIIDMMDeviceEnumerator   = syscall.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
	wasapiIIDAudioClient          = syscall.GUID{Data1: 0x1cb9ad4c, Data2: 0xdbfa, Data3: 0x4c32, Data4: [8]byte{0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}}
	wasapiIIDAudioCaptureClient   = syscall.GUID{Data1: 0xc8adbd64, Data2: 0xe71e, Data3: 0x48a0, Data4: [8]byte{0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}}
	wasapiIIDAudioRenderClient    = syscall.GUID{Data1: 0xf294acfc, Data2: 0x3146, Data3: 0x4483, Data4: [8]byte{0xa7, 0xbf, 0xad, 0xdc, 0xa7, 0xc2, 0x60, 0xe2}}
	wasapiPKeyDeviceFriendlyName  = wasapiPropertyKey{fmtid: syscall.GUID{Data1: 0xa45c254e, Data2: 0xdf1c, Data3: 0x4efd, Data4: [8]byte{0x80, 0x20, 0x67, 0xd1, 0x46, 0xa8, 0x50, 0xe0}}, pid: 14}
)

// NewWASAPIDeviceRegistry returns the Windows endpoint registry. All COM and
// WASAPI work is delayed until a registry operation is invoked.
func NewWASAPIDeviceRegistry() DeviceRegistry { return newWASAPIDeviceRegistry() }

func newWASAPIDeviceRegistry() *wasapiDeviceRegistry {
	return &wasapiDeviceRegistry{
		open:   make(map[DeviceID]bool),
		hidden: make(map[DeviceID]bool),
	}
}

type wasapiDeviceRegistry struct {
	mu           sync.Mutex
	open         map[DeviceID]bool
	hidden       map[DeviceID]bool
	listCalls    int
	defaultCalls int
	openCount    int
	releaseCount int
}

type wasapiFlow struct {
	value     uint32
	direction Direction
}

var wasapiFlows = [...]wasapiFlow{
	{value: mmdeviceDataFlowCapture, direction: DirectionInput},
	{value: mmdeviceDataFlowRender, direction: DirectionOutput},
}

// List returns a fresh active-endpoint snapshot. Endpoint IDs come directly
// from IMMDevice::GetId; enumeration order is never part of identity.
func (r *wasapiDeviceRegistry) List() ([]Device, error) {
	r.mu.Lock()
	r.listCalls++
	r.mu.Unlock()
	enumerator, cleanup, err := newWASAPIEnumerator()
	if err != nil {
		return nil, fmt.Errorf("initialize WASAPI: %w", err)
	}
	defer func() {
		enumerator.release()
		cleanup()
	}()

	devices := make([]Device, 0)
	for _, flow := range wasapiFlows {
		listed, err := r.listFlow(enumerator, flow)
		if err != nil {
			return nil, err
		}
		devices = append(devices, listed...)
	}
	sort.SliceStable(devices, func(i, j int) bool {
		if devices[i].ID != devices[j].ID {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].Direction < devices[j].Direction
	})
	return devices, nil
}

func (r *wasapiDeviceRegistry) listFlow(enumerator wasapiCOM, flow wasapiFlow) ([]Device, error) {
	collection, err := enumerateEndpoints(enumerator, flow.value)
	if err != nil {
		hr, _ := wasapiErrorCode(err)
		if isNoDeviceHRESULT(hr) {
			return nil, nil
		}
		return nil, fmt.Errorf("enumerate WASAPI %s devices: %w", flow.direction, err)
	}
	defer collection.release()

	count, err := collection.count()
	if err != nil {
		return nil, fmt.Errorf("count WASAPI %s devices: %w", flow.direction, err)
	}
	devices := make([]Device, 0, count)
	for index := uint32(0); index < count; index++ {
		endpoint, err := collection.item(index)
		if err != nil {
			return nil, fmt.Errorf("get WASAPI %s device %d: %w", flow.direction, index, err)
		}
		nativeID, err := endpointID(endpoint)
		if err != nil {
			// An endpoint can disappear between collection creation and
			// GetId. Omit that stale snapshot entry and let the next List
			// observe the current endpoint set.
			endpoint.release()
			continue
		}
		name, err := endpointFriendlyName(endpoint)
		if err != nil {
			endpoint.release()
			return nil, fmt.Errorf("read WASAPI %s device %q name: %w", flow.direction, nativeID, err)
		}
		device, err := NewDevice(wasapiBackend, nativeID, name, flow.direction)
		endpoint.release()
		if err != nil {
			return nil, fmt.Errorf("construct WASAPI %s device %q: %w", flow.direction, nativeID, err)
		}
		if r.isHidden(device.ID) {
			continue
		}
		devices = append(devices, device)
	}
	return devices, nil
}

// Default resolves the console-role default endpoint for the requested data
// flow. This is the Windows system default role used by the CLI's audio path.
func (r *wasapiDeviceRegistry) Default(direction Direction) (Device, error) {
	r.mu.Lock()
	r.defaultCalls++
	r.mu.Unlock()
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	enumerator, cleanup, err := newWASAPIEnumerator()
	if err != nil {
		return Device{}, fmt.Errorf("initialize WASAPI: %w", err)
	}
	defer func() {
		enumerator.release()
		cleanup()
	}()

	var endpointPtr unsafe.Pointer
	flow := flowForDirection(direction)
	hresult, callErr := enumerator.call(4, uintptr(flow), roleConsole, uintptr(unsafe.Pointer(&endpointPtr)))
	if callErr != nil {
		if isNoDeviceHRESULT(hresult) {
			return Device{}, NewNoDefaultDeviceError(direction)
		}
		return Device{}, fmt.Errorf("get WASAPI default %s endpoint: %w", direction, callErr)
	}
	endpoint := wasapiCOM{ptr: endpointPtr}
	defer endpoint.release()
	device, err := endpointMetadata(endpoint, direction)
	if err != nil {
		return Device{}, err
	}
	if r.isHidden(device.ID) {
		return Device{}, NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

// Open activates only the endpoint represented by id. There is deliberately
// no default fallback: an ID selected from an earlier snapshot either opens
// that endpoint or returns a typed not-found error.
func (r *wasapiDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	backend, nativeID, err := ParseDeviceID(id)
	if err != nil {
		return nil, err
	}
	if backend != wasapiBackend {
		return nil, NewDeviceNotFoundError(id)
	}
	if r.isHidden(id) {
		return nil, NewDeviceNotFoundError(id)
	}

	direction, err := r.findDirection(nativeID)
	if err != nil {
		return nil, err
	}
	if !r.reserve(id) {
		return nil, NewDeviceInUseError(id)
	}
	opened, err := openWASAPIEndpoint(nativeID, direction)
	if err != nil {
		r.releaseReservation(id)
		return nil, mapWASAPIOpenError(id, "open endpoint", err)
	}
	r.mu.Lock()
	r.openCount++
	r.mu.Unlock()
	return &wasapiOpenedDevice{registry: r, id: id, direction: direction, client: opened.client, service: opened.service}, nil
}

func (r *wasapiDeviceRegistry) findDirection(nativeID string) (Direction, error) {
	enumerator, cleanup, err := newWASAPIEnumerator()
	if err != nil {
		return "", fmt.Errorf("initialize WASAPI: %w", err)
	}
	defer func() {
		enumerator.release()
		cleanup()
	}()
	for _, flow := range wasapiFlows {
		collection, err := enumerateEndpoints(enumerator, flow.value)
		if err != nil {
			hr, _ := wasapiErrorCode(err)
			if isNoDeviceHRESULT(hr) {
				continue
			}
			return "", fmt.Errorf("enumerate WASAPI %s devices: %w", flow.direction, err)
		}
		count, err := collection.count()
		if err != nil {
			collection.release()
			return "", fmt.Errorf("count WASAPI %s devices: %w", flow.direction, err)
		}
		for index := uint32(0); index < count; index++ {
			endpoint, err := collection.item(index)
			if err != nil {
				continue
			}
			candidate, idErr := endpointID(endpoint)
			endpoint.release()
			if idErr == nil && candidate == nativeID {
				collection.release()
				return flow.direction, nil
			}
		}
		collection.release()
	}
	return "", NewDeviceNotFoundError(wasapiBackend + ":" + nativeID)
}

func (r *wasapiDeviceRegistry) reserve(id DeviceID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hidden[id] || r.open[id] {
		return false
	}
	r.open[id] = true
	return true
}

func (r *wasapiDeviceRegistry) release(id DeviceID) {
	r.mu.Lock()
	delete(r.open, id)
	r.releaseCount++
	r.mu.Unlock()
}

func (r *wasapiDeviceRegistry) releaseReservation(id DeviceID) {
	r.mu.Lock()
	delete(r.open, id)
	r.mu.Unlock()
}

func (r *wasapiDeviceRegistry) observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return DeviceRegistryObservations{
		ListCalls:    r.listCalls,
		DefaultCalls: r.defaultCalls,
		OpenCount:    r.openCount,
		ReleaseCount: r.releaseCount,
	}
}

func (r *wasapiDeviceRegistry) isHidden(id DeviceID) bool {
	r.mu.Lock()
	hidden := r.hidden[id]
	r.mu.Unlock()
	return hidden
}

// hideForTest lets the shared conformance fixture model a device disappearing
// after enumeration without changing the production registry contract.
func (r *wasapiDeviceRegistry) hideForTest(id DeviceID) {
	r.mu.Lock()
	r.hidden[id] = true
	r.mu.Unlock()
}

type wasapiOpenedDevice struct {
	registry  *wasapiDeviceRegistry
	id        DeviceID
	direction Direction
	client    wasapiCOM
	service   wasapiCOM
	mu        sync.Mutex
	closed    bool
}

// verifyDataPathForTest observes the live client rather than only checking
// that COM activation returned a handle. Capture must expose a packet, while
// render is fed an explicit silent packet and must accept it into the buffer.
func (d *wasapiOpenedDevice) verifyDataPathForTest() error {
	cleanup, err := initializeCOM()
	if err != nil {
		return err
	}
	defer cleanup()

	if d.direction == DirectionInput {
		for attempt := 0; attempt < 40; attempt++ {
			var packets uint32
			if _, err := d.service.call(audioCaptureClientVTableGetNextPacketSize, uintptr(unsafe.Pointer(&packets))); err != nil {
				return fmt.Errorf("read WASAPI capture packet size: %w", err)
			}
			if packets > 0 {
				return nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		return fmt.Errorf("WASAPI capture client produced no packets")
	}

	var bufferSize, padding uint32
	if _, err := d.client.call(audioClientVTableGetBufferSize, uintptr(unsafe.Pointer(&bufferSize))); err != nil {
		return fmt.Errorf("read WASAPI render buffer size: %w", err)
	}
	if bufferSize == 0 {
		return fmt.Errorf("WASAPI render buffer size is zero")
	}
	if _, err := d.client.call(audioClientVTableGetCurrentPadding, uintptr(unsafe.Pointer(&padding))); err != nil {
		return fmt.Errorf("read WASAPI render padding: %w", err)
	}
	for attempt := 0; attempt < 40 && padding >= bufferSize; attempt++ {
		time.Sleep(25 * time.Millisecond)
		if _, err := d.client.call(audioClientVTableGetCurrentPadding, uintptr(unsafe.Pointer(&padding))); err != nil {
			return fmt.Errorf("read WASAPI render padding: %w", err)
		}
	}
	if padding >= bufferSize {
		return fmt.Errorf("WASAPI render buffer has no writable frames")
	}
	frames := bufferSize - padding
	var buffer unsafe.Pointer
	if _, err := d.service.call(audioRenderClientVTableGetBuffer, uintptr(frames), uintptr(unsafe.Pointer(&buffer))); err != nil {
		return fmt.Errorf("acquire WASAPI render buffer: %w", err)
	}
	if _, err := d.service.call(audioRenderClientVTableReleaseBuffer, uintptr(frames), audclntBufferFlagsSilent); err != nil {
		return fmt.Errorf("release WASAPI render buffer: %w", err)
	}
	return nil
}

func (d *wasapiOpenedDevice) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	client, service := d.client, d.service
	d.client.ptr = nil
	d.service.ptr = nil
	d.mu.Unlock()
	defer d.registry.release(d.id)

	cleanup, initErr := initializeCOM()
	if initErr != nil {
		service.release()
		client.release()
		return initErr
	}
	defer cleanup()

	var stopErr error
	if client.ptr != nil {
		hresult, err := client.call(audioClientVTableStop)
		if err != nil && !isNoDeviceHRESULT(hresult) {
			stopErr = fmt.Errorf("stop WASAPI endpoint %q: %w", d.id, err)
		}
	}
	service.release()
	client.release()
	return stopErr
}

type openedWASAPIEndpoint struct {
	client  wasapiCOM
	service wasapiCOM
}

func openWASAPIEndpoint(nativeID string, direction Direction) (openedWASAPIEndpoint, error) {
	enumerator, cleanup, err := newWASAPIEnumerator()
	if err != nil {
		return openedWASAPIEndpoint{}, err
	}
	defer func() {
		enumerator.release()
		cleanup()
	}()

	nativeIDPtr, err := syscall.UTF16PtrFromString(nativeID)
	if err != nil {
		return openedWASAPIEndpoint{}, NewDeviceNotFoundError(wasapiBackend + ":" + nativeID)
	}
	var endpointPtr unsafe.Pointer
	hresult, callErr := enumerator.call(5, uintptr(unsafe.Pointer(nativeIDPtr)), uintptr(unsafe.Pointer(&endpointPtr)))
	if callErr != nil {
		return openedWASAPIEndpoint{}, wasapiHRESULTWithCode{hr: hresult, err: callErr, operation: "get endpoint"}
	}
	endpoint := wasapiCOM{ptr: endpointPtr}
	defer endpoint.release()

	iid := wasapiIIDAudioClient
	var clientPtr unsafe.Pointer
	hresult, callErr = endpoint.call(immDeviceVTableActivate, uintptr(unsafe.Pointer(&iid)), clsctxAll, 0, uintptr(unsafe.Pointer(&clientPtr)))
	if callErr != nil {
		return openedWASAPIEndpoint{}, wasapiHRESULTWithCode{hr: hresult, err: callErr, operation: "activate audio client"}
	}
	client := wasapiCOM{ptr: clientPtr}
	defer func() {
		if client.ptr != nil {
			client.release()
		}
	}()

	var mixFormat unsafe.Pointer
	hresult, callErr = client.call(audioClientVTableGetMixFormat, uintptr(unsafe.Pointer(&mixFormat)))
	if callErr != nil {
		return openedWASAPIEndpoint{}, wasapiHRESULTWithCode{hr: hresult, err: callErr, operation: "get mix format"}
	}
	if mixFormat == nil {
		return openedWASAPIEndpoint{}, fmt.Errorf("WASAPI returned an empty mix format")
	}
	defer wasapiCoTaskMemFree.Call(uintptr(mixFormat))

	hresult, callErr = client.call(audioClientVTableInitialize, shareModeShared, 0, 0, 0, uintptr(mixFormat), 0)
	if callErr != nil {
		return openedWASAPIEndpoint{}, wasapiHRESULTWithCode{hr: hresult, err: callErr, operation: "initialize audio client"}
	}

	serviceIID := wasapiIIDAudioRenderClient
	if direction == DirectionInput {
		serviceIID = wasapiIIDAudioCaptureClient
	}
	var servicePtr unsafe.Pointer
	hresult, callErr = client.call(audioClientVTableGetService, uintptr(unsafe.Pointer(&serviceIID)), uintptr(unsafe.Pointer(&servicePtr)))
	if callErr != nil {
		return openedWASAPIEndpoint{}, wasapiHRESULTWithCode{hr: hresult, err: callErr, operation: "get audio data client"}
	}
	service := wasapiCOM{ptr: servicePtr}
	defer func() {
		if service.ptr != nil {
			service.release()
		}
	}()

	hresult, callErr = client.call(audioClientVTableStart)
	if callErr != nil {
		return openedWASAPIEndpoint{}, wasapiHRESULTWithCode{hr: hresult, err: callErr, operation: "start audio client"}
	}
	opened := openedWASAPIEndpoint{client: client, service: service}
	client.ptr = nil
	service.ptr = nil
	return opened, nil
}

func mapWASAPIOpenError(id DeviceID, operation string, err error) error {
	var coded wasapiHRESULTWithCode
	if !asWASAPIHRESULT(err, &coded) {
		return fmt.Errorf("WASAPI %s %q: %w", operation, id, err)
	}
	switch coded.hr {
	case audclntDeviceInUse:
		return NewDeviceInUseError(id)
	case hresultNotFound, audclntDeviceInvalidated:
		return NewDeviceNotFoundError(id)
	default:
		if coded.operation != "" {
			operation = coded.operation
		}
		return fmt.Errorf("WASAPI %s %q: %w", operation, id, coded.err)
	}
}

type wasapiHRESULTWithCode struct {
	hr        uint32
	err       error
	operation string
}

func (e wasapiHRESULTWithCode) Error() string { return e.err.Error() }
func (e wasapiHRESULTWithCode) Unwrap() error { return e.err }

func asWASAPIHRESULT(err error, target *wasapiHRESULTWithCode) bool {
	if err == nil {
		return false
	}
	if coded, ok := err.(wasapiHRESULTWithCode); ok {
		*target = coded
		return true
	}
	return false
}

func wasapiErrorCode(err error) (uint32, bool) {
	var coded wasapiHRESULTWithCode
	if !asWASAPIHRESULT(err, &coded) {
		return 0, false
	}
	return coded.hr, true
}

func isNoDeviceHRESULT(hr uint32) bool {
	return hr == hresultNotFound || hr == audclntDeviceInvalidated
}

func flowForDirection(direction Direction) uint32 {
	if direction == DirectionInput {
		return mmdeviceDataFlowCapture
	}
	return mmdeviceDataFlowRender
}

func endpointMetadata(endpoint wasapiCOM, direction Direction) (Device, error) {
	nativeID, err := endpointID(endpoint)
	if err != nil {
		return Device{}, err
	}
	name, err := endpointFriendlyName(endpoint)
	if err != nil {
		return Device{}, fmt.Errorf("read WASAPI device %q name: %w", nativeID, err)
	}
	device, err := NewDevice(wasapiBackend, nativeID, name, direction)
	if err != nil {
		return Device{}, fmt.Errorf("construct WASAPI device %q: %w", nativeID, err)
	}
	return device, nil
}

func enumerateEndpoints(enumerator wasapiCOM, flow uint32) (wasapiCOM, error) {
	var collectionPtr unsafe.Pointer
	hresult, err := enumerator.call(immDeviceEnumeratorVTable, uintptr(flow), deviceStateActive, uintptr(unsafe.Pointer(&collectionPtr)))
	if err != nil {
		return wasapiCOM{}, wasapiHRESULTWithCode{hr: hresult, err: err}
	}
	return wasapiCOM{ptr: collectionPtr}, nil
}

func (c wasapiCOM) count() (uint32, error) {
	var count uint32
	hresult, err := c.call(immDeviceCollectionVTable, uintptr(unsafe.Pointer(&count)))
	if err != nil {
		return 0, wasapiHRESULTWithCode{hr: hresult, err: err}
	}
	return count, nil
}

func (c wasapiCOM) item(index uint32) (wasapiCOM, error) {
	var endpointPtr unsafe.Pointer
	hresult, err := c.call(immDeviceCollectionVTable+1, uintptr(index), uintptr(unsafe.Pointer(&endpointPtr)))
	if err != nil {
		return wasapiCOM{}, wasapiHRESULTWithCode{hr: hresult, err: err}
	}
	return wasapiCOM{ptr: endpointPtr}, nil
}

func endpointID(endpoint wasapiCOM) (string, error) {
	var raw unsafe.Pointer
	_, err := endpoint.call(immDeviceVTableGetID, uintptr(unsafe.Pointer(&raw)))
	if err != nil {
		return "", err
	}
	if raw == nil {
		return "", fmt.Errorf("WASAPI returned an empty endpoint ID")
	}
	defer wasapiCoTaskMemFree.Call(uintptr(raw))
	return utf16PtrString((*uint16)(raw)), nil
}

func endpointFriendlyName(endpoint wasapiCOM) (string, error) {
	var storePtr unsafe.Pointer
	_, err := endpoint.call(immDeviceVTablePropertyStore, 0, uintptr(unsafe.Pointer(&storePtr)))
	if err != nil {
		return "", err
	}
	store := wasapiCOM{ptr: storePtr}
	defer store.release()

	var value wasapiPropVariant
	defer wasapiPropVariantClear.Call(uintptr(unsafe.Pointer(&value)))
	_, err = store.call(propertyStoreVTableGetValue, uintptr(unsafe.Pointer(&wasapiPKeyDeviceFriendlyName)), uintptr(unsafe.Pointer(&value)))
	if err != nil {
		return "", err
	}
	if value.vt != vtLPWSTR || value.value == nil {
		return "", fmt.Errorf("friendly name has PROPVARIANT type %d, want VT_LPWSTR", value.vt)
	}
	name := strings.TrimSpace(utf16PtrString((*uint16)(value.value)))
	if name == "" {
		return "", fmt.Errorf("friendly name is empty")
	}
	return name, nil
}

type wasapiPropertyKey struct {
	fmtid syscall.GUID
	pid   uint32
}

type wasapiPropVariant struct {
	vt       uint16
	reserved [3]uint16
	value    unsafe.Pointer
}

func utf16PtrString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}
	length := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(ptr), uintptr(length)*unsafe.Sizeof(*ptr))) != 0 {
		length++
	}
	return syscall.UTF16ToString(unsafe.Slice(ptr, length))
}

type wasapiCOM struct{ ptr unsafe.Pointer }

func (c wasapiCOM) call(index int, args ...uintptr) (uint32, error) {
	if c.ptr == nil {
		return 0x80004003, wasapiHRESULT(0x80004003)
	}
	method := c.vtableMethod(index)
	callArgs := make([]uintptr, 1, len(args)+1)
	callArgs[0] = uintptr(c.ptr)
	callArgs = append(callArgs, args...)
	r1, _, _ := syscall.SyscallN(method, callArgs...)
	hresult := uint32(r1)
	if int32(hresult) < 0 {
		return hresult, wasapiHRESULT(hresult)
	}
	return hresult, nil
}

func (c wasapiCOM) vtableMethod(index int) uintptr {
	vtable := *(*unsafe.Pointer)(c.ptr)
	return *(*uintptr)(unsafe.Add(vtable, uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

func (c *wasapiCOM) release() {
	if c == nil || c.ptr == nil {
		return
	}
	method := c.vtableMethod(2)
	_, _, _ = syscall.SyscallN(method, uintptr(c.ptr))
	c.ptr = nil
}

type wasapiHRESULT uint32

func (e wasapiHRESULT) Error() string {
	return fmt.Sprintf("WASAPI call failed with HRESULT 0x%08x", uint32(e))
}

func initializeCOM() (func(), error) {
	r1, _, _ := wasapiCoInitializeEx.Call(0, coinitMultithreaded)
	hresult := uint32(r1)
	if int32(hresult) < 0 {
		return nil, wasapiHRESULT(hresult)
	}
	return func() { wasapiCoUninitialize.Call() }, nil
}

func newWASAPIEnumerator() (wasapiCOM, func(), error) {
	cleanup, err := initializeCOM()
	if err != nil {
		return wasapiCOM{}, nil, err
	}
	var enumeratorPtr unsafe.Pointer
	hresult, _, _ := wasapiCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&wasapiCLSIDMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&wasapiIIDMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enumeratorPtr)),
	)
	if int32(uint32(hresult)) < 0 || enumeratorPtr == nil {
		cleanup()
		if enumeratorPtr == nil && int32(uint32(hresult)) >= 0 {
			return wasapiCOM{}, nil, fmt.Errorf("WASAPI returned an empty device enumerator")
		}
		return wasapiCOM{}, nil, wasapiHRESULT(uint32(hresult))
	}
	return wasapiCOM{ptr: enumeratorPtr}, cleanup, nil
}

var _ DeviceRegistry = (*wasapiDeviceRegistry)(nil)
var _ OpenedDevice = (*wasapiOpenedDevice)(nil)
