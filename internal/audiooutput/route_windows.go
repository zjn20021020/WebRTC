package audiooutput

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32          = windows.NewLazySystemDLL("ole32.dll")
	initialize     = ole32.NewProc("CoInitializeEx")
	uninitialize   = ole32.NewProc("CoUninitialize")
	createInstance = ole32.NewProc("CoCreateInstance")
	clearVariant   = ole32.NewProc("PropVariantClear")
)

type propertyKey struct {
	format windows.GUID
	id     uint32
}

type propertyVariant struct {
	kind     uint16
	reserved [3]uint16
	value    [2]uintptr
}

type comObject struct{ methods *[8]uintptr }

func release(object *comObject) {
	if object != nil {
		syscall.SyscallN(object.methods[2], uintptr(unsafe.Pointer(object)))
	}
}

func property(store *comObject, key propertyKey) (propertyVariant, bool) {
	var value propertyVariant
	result, _, _ := syscall.SyscallN(store.methods[5], uintptr(unsafe.Pointer(store)), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&value)))
	runtime.KeepAlive(store)
	return value, int32(result) >= 0
}

func Current() Route {
	unknown := Route{Kind: "unknown", Source: "windows_endpoint", Reason: "endpoint_unavailable"}
	// COM apartment state belongs to an OS thread, including cleanup calls.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, _ := initialize.Call(0, 0)
	if int32(result) >= 0 {
		defer uninitialize.Call()
	} else if uint32(result) != 0x80010106 { // RPC_E_CHANGED_MODE: use the existing apartment.
		return unknown
	}
	classID := windows.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
	interfaceID := windows.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
	var enumerator *comObject
	result, _, _ = createInstance.Call(uintptr(unsafe.Pointer(&classID)), 0, 1, uintptr(unsafe.Pointer(&interfaceID)), uintptr(unsafe.Pointer(&enumerator)))
	if int32(result) < 0 || enumerator == nil {
		return unknown
	}
	defer release(enumerator)
	var device *comObject
	// IMMDeviceEnumerator.GetDefaultAudioEndpoint(eRender, eConsole).
	result, _, _ = syscall.SyscallN(enumerator.methods[4], uintptr(unsafe.Pointer(enumerator)), 0, 0, uintptr(unsafe.Pointer(&device)))
	if int32(result) < 0 || device == nil {
		return unknown
	}
	defer release(device)
	var store *comObject
	result, _, _ = syscall.SyscallN(device.methods[4], uintptr(unsafe.Pointer(device)), 0, uintptr(unsafe.Pointer(&store)))
	if int32(result) < 0 || store == nil {
		return unknown
	}
	defer release(store)
	nameKey := propertyKey{format: windows.GUID{Data1: 0xa45c254e, Data2: 0xdf1c, Data3: 0x4efd, Data4: [8]byte{0x80, 0x20, 0x67, 0xd1, 0x46, 0xa8, 0x50, 0xe0}}, id: 14}
	name, ok := property(store, nameKey)
	if ok {
		if name.kind == 31 && name.value[0] != 0 { // VT_LPWSTR
			unknown.Name = windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&name.value[0])))
		}
		clearVariant.Call(uintptr(unsafe.Pointer(&name)))
	}
	formKey := propertyKey{format: windows.GUID{Data1: 0x1da5d803, Data2: 0xd492, Data3: 0x4edd, Data4: [8]byte{0x8c, 0x23, 0xe0, 0xc0, 0xff, 0xee, 0x7f, 0x0e}}}
	form, ok := property(store, formKey)
	if !ok {
		return unknown
	}
	defer clearVariant.Call(uintptr(unsafe.Pointer(&form)))
	if form.kind != 19 { // VT_UI4
		return unknown
	}
	switch uint32(form.value[0]) {
	case 3, 5: // Headphones, Headset.
		unknown.Kind, unknown.Reason = "headphones", "form_factor"
	case 1: // Speakers; an analog combo jack may still be reported this way.
		unknown.Kind, unknown.Reason = "speakers", "form_factor"
	default:
		unknown.Reason = "ambiguous_form_factor"
	}
	return unknown
}
