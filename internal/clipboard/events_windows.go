//go:build windows

package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	createEvent      = kernel32.NewProc("CreateEventW")
	setEvent         = kernel32.NewProc("SetEvent")
	closeHandle      = kernel32.NewProc("CloseHandle")
	getProcessHeap   = kernel32.NewProc("GetProcessHeap")
	heapAlloc        = kernel32.NewProc("HeapAlloc")
	heapFree         = kernel32.NewProc("HeapFree")
	user32           = syscall.NewLazyDLL("user32.dll")
	msgWait          = user32.NewProc("MsgWaitForMultipleObjectsEx")
	peekMessage      = user32.NewProc("PeekMessageW")
	translateMessage = user32.NewProc("TranslateMessage")
	dispatchMessage  = user32.NewProc("DispatchMessageW")
	createDispatcher = syscall.NewLazyDLL("CoreMessaging.dll").NewProc("CreateDispatcherQueueController")

	iidUnknown = syscall.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidAgile   = syscall.GUID{Data1: 0x94ea2b94, Data2: 0xe9cc, Data3: 0x49e0, Data4: [8]byte{0xc0, 0xff, 0xee, 0x64, 0xca, 0x8f, 0x5b, 0x90}}
	// Parameterized IIDs follow the WinRT signature/UUID-v5 algorithm.
	iidHistoryHandler   = syscall.GUID{Data1: 0xdf4aac23, Data2: 0x4002, Data3: 0x5d4c, Data4: [8]byte{0xa2, 0x37, 0x25, 0x26, 0xe3, 0x44, 0x97, 0x8d}}
	iidEnabledHandler   = syscall.GUID{Data1: 0xc50898f6, Data2: 0xc536, Data3: 0x5f47, Data4: [8]byte{0x85, 0x83, 0x8b, 0x2c, 0x24, 0x38, 0xa1, 0x3b}}
	iidHistoryCompleted = syscall.GUID{Data1: 0x841da82d, Data2: 0xa32c, Data3: 0x5997, Data4: [8]byte{0x84, 0x50, 0xf5, 0x4a, 0xf1, 0xd5, 0x47, 0x7e}}
	iidActionCompleted  = syscall.GUID{Data1: 0xa4ed5c81, Data2: 0x76c9, Data3: 0x40bd, Data4: [8]byte{0x8b, 0xe6, 0xb1, 0xd9, 0x0f, 0xb2, 0x0a, 0xe7}}
	delegateMu          sync.Mutex
	delegates           = make(map[uintptr]*nativeDelegate)
	delegateVTable      = [4]uintptr{
		syscall.NewCallback(delegateQuery), syscall.NewCallback(delegateAddRef),
		syscall.NewCallback(delegateRelease), syscall.NewCallback(delegateInvoke),
	}
)

// The COM object is native heap memory. Go state stays rooted in delegates until
// both our owner reference and all references retained by Windows are released.
// This also keeps late async callbacks safe after cancellation.
type nativeDelegate struct {
	address, heap, signal uintptr
	iid                   syscall.GUID
	refs                  uint32
}

func newSignal() (uintptr, error) {
	h, _, err := createEvent.Call(0, 0, 0, 0) // auto-reset, initially unsignaled
	if h == 0 {
		return 0, fmt.Errorf("CreateEvent: %v", err)
	}
	return h, nil
}

func newDelegate(iid syscall.GUID) (*nativeDelegate, error) {
	signal, err := newSignal()
	if err != nil {
		return nil, err
	}
	heap, _, _ := getProcessHeap.Call()
	address, _, callErr := heapAlloc.Call(heap, 8, unsafe.Sizeof(uintptr(0)))
	if address == 0 {
		closeHandle.Call(signal)
		return nil, fmt.Errorf("HeapAlloc: %v", callErr)
	}
	vtable := uintptr(unsafe.Pointer(&delegateVTable[0]))
	moveMemory.Call(address, uintptr(unsafe.Pointer(&vtable)), unsafe.Sizeof(vtable))
	d := &nativeDelegate{address: address, heap: heap, signal: signal, iid: iid, refs: 1}
	delegateMu.Lock()
	delegates[address] = d
	delegateMu.Unlock()
	return d, nil
}

//go:uintptrescapes
func delegateQuery(this, riid, output uintptr) uintptr {
	if output == 0 || riid == 0 {
		return 0x80004003
	} // E_POINTER
	var zero uintptr
	moveMemory.Call(output, uintptr(unsafe.Pointer(&zero)), unsafe.Sizeof(zero))
	var iid syscall.GUID
	moveMemory.Call(uintptr(unsafe.Pointer(&iid)), riid, unsafe.Sizeof(iid))
	delegateMu.Lock()
	defer delegateMu.Unlock()
	d := delegates[this]
	if d == nil || (iid != iidUnknown && iid != iidAgile && iid != d.iid) {
		return 0x80004002 // E_NOINTERFACE
	}
	d.refs++
	moveMemory.Call(output, uintptr(unsafe.Pointer(&this)), unsafe.Sizeof(this))
	return 0
}
func delegateAddRef(this uintptr) uintptr {
	delegateMu.Lock()
	defer delegateMu.Unlock()
	if d := delegates[this]; d != nil {
		d.refs++
		return uintptr(d.refs)
	}
	return 0
}
func delegateRelease(this uintptr) uintptr {
	delegateMu.Lock()
	defer delegateMu.Unlock()
	d := delegates[this]
	if d == nil {
		return 0
	}
	d.refs--
	if d.refs == 0 {
		delete(delegates, this)
		closeHandle.Call(d.signal)
		heapFree.Call(d.heap, 0, d.address)
	}
	return uintptr(d.refs)
}
func delegateInvoke(this, sender, args uintptr) uintptr {
	delegateMu.Lock()
	defer delegateMu.Unlock()
	if d := delegates[this]; d != nil {
		// Do not read clipboard or wait inside a Windows callback.
		setEvent.Call(d.signal)
	}
	return 0
}

// MSG layout, including pointer alignment, matches the Windows SDK.
type winMessage struct {
	hwnd           uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	x, y           int32
	private        uint32
}

// waitSignals blocks on real events and pumps UI/COM messages on this thread.
// There is no timeout-based polling. A context cancellation has its own event.
func waitSignals(ctx context.Context, signals ...uintptr) (int, error) {
	cancelSignal, err := newSignal()
	if err != nil {
		return 0, err
	}
	defer closeHandle.Call(cancelSignal)
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			setEvent.Call(cancelSignal)
		case <-stop:
		}
	}()
	defer func() { close(stop); <-joined }()
	handles := append(append([]uintptr(nil), signals...), cancelSignal)
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		result, _, callErr := msgWait.Call(uintptr(len(handles)), uintptr(unsafe.Pointer(&handles[0])),
			0xffffffff, 0x04ff, 4) // INFINITE, QS_ALLINPUT, MWMO_INPUTAVAILABLE
		switch {
		case result < uintptr(len(signals)):
			return int(result), nil
		case result == uintptr(len(signals)):
			return 0, ctx.Err()
		case result == uintptr(len(handles)):
			for {
				var msg winMessage
				ok, _, _ := peekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1)
				if ok == 0 {
					break
				}
				if msg.message == 0x0012 {
					return 0, context.Canceled
				} // WM_QUIT
				translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
				dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
				if err := ctx.Err(); err != nil {
					return 0, err
				}
			}
		default:
			return 0, fmt.Errorf("MsgWaitForMultipleObjectsEx: result=%#x: %v", result, callErr)
		}
	}
}

func startDispatcher() (*comObject, error) {
	// The struct-by-value native call below uses the Windows x64 ABI.
	if runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("эта версия CLI поддерживает Windows x64")
	}
	if err := createDispatcher.Find(); err != nil {
		return nil, err
	}
	options := [3]uint32{12, 2, 0} // size, CURRENT_THREAD, COM_NONE (already STA)
	var controller *comObject
	hr, _, _ := createDispatcher.Call(uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&controller)))
	return controller, checkHR(hr)
}
func stopDispatcher(controller *comObject) error {
	defer controller.release()
	var action *comObject
	if err := controller.invoke(7, uintptr(unsafe.Pointer(&action))); err != nil {
		return err
	}
	defer action.release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := awaitOperation(ctx, action, iidActionCompleted); err != nil {
		return err
	}
	return action.invoke(8)
}

func awaitOperation(ctx context.Context, operation *comObject, iid syscall.GUID) error {
	d, err := newDelegate(iid)
	if err != nil {
		return err
	}
	defer delegateRelease(d.address)
	if err := operation.invoke(6, d.address); err != nil {
		return err
	} // put_Completed
	if _, err := waitSignals(ctx, d.signal); err != nil {
		var info *comObject
		if operation.invoke(0, uintptr(unsafe.Pointer(&iidAsyncInfo)), uintptr(unsafe.Pointer(&info))) == nil {
			_ = info.invoke(9) // Cancel; Windows retains the delegate until safe.
			info.release()
		}
		return err
	}
	return nil // GetResults reports asynchronous errors/cancellation to caller.
}

type historySubscription struct {
	clipboard                  *comObject
	history, enabled           *nativeDelegate
	historyToken, enabledToken int64
}

func (r *historyReader) subscribe() (*historySubscription, error) {
	history, err := newDelegate(iidHistoryHandler)
	if err != nil {
		return nil, err
	}
	enabled, err := newDelegate(iidEnabledHandler)
	if err != nil {
		delegateRelease(history.address)
		return nil, err
	}
	s := &historySubscription{clipboard: r.clipboard, history: history, enabled: enabled}
	if err := r.clipboard.invoke(13, history.address, uintptr(unsafe.Pointer(&s.historyToken))); err != nil {
		delegateRelease(history.address)
		delegateRelease(enabled.address)
		return nil, err
	}
	if err := r.clipboard.invoke(17, enabled.address, uintptr(unsafe.Pointer(&s.enabledToken))); err != nil {
		_ = r.clipboard.invoke(14, uintptr(s.historyToken))
		delegateRelease(history.address)
		delegateRelease(enabled.address)
		return nil, err
	}
	return s, nil
}
func (s *historySubscription) close() error {
	first := s.clipboard.invoke(14, uintptr(s.historyToken))
	second := s.clipboard.invoke(18, uintptr(s.enabledToken))
	delegateRelease(s.history.address)
	delegateRelease(s.enabled.address)
	if first != nil {
		return first
	}
	return second
}
