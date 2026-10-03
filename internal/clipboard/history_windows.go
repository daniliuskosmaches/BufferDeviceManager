//go:build windows
// +build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// Minimal Windows Runtime ABI bindings. No PowerShell, cgo or external modules.
// Interface layout: Microsoft Windows SDK / microsoft/windows-rs bindings.
// IInspectable has six slots; IAsyncOperation.GetResults is slot 8.
var (
	moveMemory                = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlMoveMemory")
	combase                   = syscall.NewLazyDLL("combase.dll")
	roInitialize              = combase.NewProc("RoInitialize")
	roUninitialize            = combase.NewProc("RoUninitialize")
	roGetActivationFactory    = combase.NewProc("RoGetActivationFactory")
	windowsCreateString       = combase.NewProc("WindowsCreateString")
	windowsDeleteString       = combase.NewProc("WindowsDeleteString")
	windowsGetStringRawBuffer = combase.NewProc("WindowsGetStringRawBuffer")

	iidClipboardStatics2 = syscall.GUID{Data1: 0xd2ac1b6a, Data2: 0xd29f, Data3: 0x554b,
		Data4: [8]byte{0xb3, 0x03, 0xf0, 0x45, 0x23, 0x45, 0xfe, 0x02}}
	iidAsyncInfo = syscall.GUID{Data1: 0x00000036,
		Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

	errHistoryDisabled = errors.New("история Win+V выключена: нажмите Win+V и включите её; ожидание")
	errHistoryDenied   = errors.New("Windows отказала в доступе к истории Win+V; повторная попытка при следующем уведомлении")
)

type comObject struct {
	vtable *[19]uintptr
}

// uintptr arguments may contain Go pointers; keep them alive and on the heap
// throughout the native call, including nested calls made by the Go runtime.
//
//go:uintptrescapes
func (o *comObject) invoke(slot int, args ...uintptr) error {
	args = append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	hr, _, _ := syscall.SyscallN(o.vtable[slot], args...)
	return checkHR(hr)
}

func checkHR(hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("Windows HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

func (o *comObject) release() {
	if o != nil {
		_ = o.invoke(2)
	}
}

type historyReader struct {
	clipboard *comObject
}

func newHistoryReader() (*historyReader, error) {
	for _, proc := range []*syscall.LazyProc{roInitialize, roUninitialize,
		roGetActivationFactory, windowsCreateString, windowsDeleteString, windowsGetStringRawBuffer, moveMemory} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("нужна Windows 10 1809 или новее: %w", err)
		}
	}
	hr, _, _ := roInitialize.Call(0) // RO_INIT_SINGLETHREADED; dispatcher pumps messages
	if err := checkHR(hr); err != nil {
		return nil, fmt.Errorf("RoInitialize: %w", err)
	}
	ready := false
	defer func() {
		if !ready {
			roUninitialize.Call()
		}
	}()

	name := utf16.Encode([]rune("Windows.ApplicationModel.DataTransfer.Clipboard"))
	var className uintptr // HSTRING
	hr, _, _ = windowsCreateString.Call(uintptr(unsafe.Pointer(&name[0])),
		uintptr(len(name)), uintptr(unsafe.Pointer(&className)))
	if err := checkHR(hr); err != nil {
		return nil, fmt.Errorf("WindowsCreateString: %w", err)
	}
	defer windowsDeleteString.Call(className)

	var clipboard *comObject
	hr, _, _ = roGetActivationFactory.Call(className,
		uintptr(unsafe.Pointer(&iidClipboardStatics2)), uintptr(unsafe.Pointer(&clipboard)))
	if err := checkHR(hr); err != nil {
		return nil, fmt.Errorf("Clipboard History API (Windows 10 1809+): %w", err)
	}
	ready = true
	return &historyReader{clipboard: clipboard}, nil
}

func (r *historyReader) close() {
	r.clipboard.release()
	roUninitialize.Call()
}

// read returns only history item IDs. It never reads or modifies their content.
// The caller must retain the last snapshot if any API call fails.
func (r *historyReader) read(ctx context.Context) ([]string, error) {
	var enabled byte // WinRT boolean is one byte.
	if err := r.clipboard.invoke(10, uintptr(unsafe.Pointer(&enabled))); err != nil {
		return nil, fmt.Errorf("IsHistoryEnabled: %w", err)
	}
	if enabled == 0 {
		return nil, errHistoryDisabled
	}
	var operation *comObject
	if err := r.clipboard.invoke(6, uintptr(unsafe.Pointer(&operation))); err != nil {
		return nil, fmt.Errorf("GetHistoryItemsAsync: %w", err)
	}
	defer operation.release()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := awaitHistory(ctx, operation)
	if err != nil {
		return nil, err
	}
	defer result.release()

	var status uint32
	if err := result.invoke(6, uintptr(unsafe.Pointer(&status))); err != nil {
		return nil, err
	}
	switch status {
	case 0: // Success
	case 1:
		return nil, errHistoryDenied
	case 2:
		return nil, errHistoryDisabled
	default:
		return nil, fmt.Errorf("неизвестный статус истории: %d", status)
	}

	var items *comObject // IVectorView<ClipboardHistoryItem>
	if err := result.invoke(7, uintptr(unsafe.Pointer(&items))); err != nil {
		return nil, err
	}
	defer items.release()
	var count uint32
	if err := items.invoke(7, uintptr(unsafe.Pointer(&count))); err != nil {
		return nil, err
	}
	ids := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		id, err := historyItemID(items, i)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func awaitHistory(ctx context.Context, operation *comObject) (*comObject, error) {
	if err := awaitOperation(ctx, operation, iidHistoryCompleted); err != nil {
		return nil, err
	}
	var result *comObject
	if err := operation.invoke(8, uintptr(unsafe.Pointer(&result))); err != nil {
		return nil, err
	}
	return result, nil
}

func historyItemID(items *comObject, index uint32) (string, error) {
	var item *comObject
	if err := items.invoke(6, uintptr(index), uintptr(unsafe.Pointer(&item))); err != nil {
		return "", err
	}
	defer item.release()
	var id uintptr // HSTRING owned by this call.
	if err := item.invoke(6, uintptr(unsafe.Pointer(&id))); err != nil {
		return "", err
	}
	defer windowsDeleteString.Call(id)
	var length uint32
	buffer, _, _ := windowsGetStringRawBuffer.Call(id, uintptr(unsafe.Pointer(&length)))
	if length == 0 || buffer == 0 {
		return "", errors.New("Windows вернула элемент истории без идентификатора")
	}
	// Copy while the HSTRING is alive, keeping its native address out of Go's
	// pointer space. The resulting Go string does not depend on native memory.
	units := make([]uint16, length)
	moveMemory.Call(uintptr(unsafe.Pointer(&units[0])), buffer, uintptr(length)*2)
	return string(utf16.Decode(units)), nil
}
