//go:build windows

package systemclipboard

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	user           = syscall.NewLazyDLL("user32.dll")
	kernel         = syscall.NewLazyDLL("kernel32.dll")
	open           = user.NewProc("OpenClipboard")
	closeClipboard = user.NewProc("CloseClipboard")
	get            = user.NewProc("GetClipboardData")
	set            = user.NewProc("SetClipboardData")
	empty          = user.NewProc("EmptyClipboard")
	available      = user.NewProc("IsClipboardFormatAvailable")
	createWindow   = user.NewProc("CreateWindowExW")
	destroyWindow  = user.NewProc("DestroyWindow")
	addListener    = user.NewProc("AddClipboardFormatListener")
	removeListener = user.NewProc("RemoveClipboardFormatListener")
	getMessage     = user.NewProc("GetMessageW")
	dispatch       = user.NewProc("DispatchMessageW")
	post           = user.NewProc("PostMessageW")
	alloc          = kernel.NewProc("GlobalAlloc")
	lock           = kernel.NewProc("GlobalLock")
	unlock         = kernel.NewProc("GlobalUnlock")
	free           = kernel.NewProc("GlobalFree")
	size           = kernel.NewProc("GlobalSize")
)

const unicodeText = 13

type Clipboard struct{}

func New() (*Clipboard, error) { return &Clipboard{}, nil }
func newWindow() (uintptr, error) {
	name, _ := syscall.UTF16PtrFromString("STATIC")
	hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0) // HWND_MESSAGE = -3
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %v", err)
	}
	return hwnd, nil
}
func (*Clipboard) Read() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if ok, _, _ := available.Call(unicodeText); ok == 0 {
		return "", nil
	}
	if ok, _, err := open.Call(0); ok == 0 {
		return "", fmt.Errorf("OpenClipboard: %v", err)
	}
	defer closeClipboard.Call()
	handle, _, err := get.Call(unicodeText)
	if handle == 0 {
		return "", fmt.Errorf("GetClipboardData: %v", err)
	}
	count, _, _ := size.Call(handle)
	if count == 0 || count > 2*65536+2 {
		return "", nil
	}
	pointer, _, err := lock.Call(handle)
	if pointer == 0 {
		return "", fmt.Errorf("GlobalLock: %v", err)
	}
	defer unlock.Call(handle)
	chars := unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), int(count/2))
	for i, v := range chars {
		if v == 0 {
			chars = chars[:i]
			break
		}
	}
	return string(utf16.Decode(chars)), nil
}
func (*Clipboard) Write(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, err := newWindow()
	if err != nil {
		return err
	}
	defer destroyWindow.Call(hwnd)
	chars, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	handle, _, err := alloc.Call(2, uintptr(len(chars)*2))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc: %v", err)
	}
	owned := true
	defer func() {
		if owned {
			free.Call(handle)
		}
	}()
	pointer, _, err := lock.Call(handle)
	if pointer == 0 {
		return fmt.Errorf("GlobalLock: %v", err)
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), len(chars)), chars)
	unlock.Call(handle)
	if ok, _, err := open.Call(hwnd); ok == 0 {
		return fmt.Errorf("OpenClipboard: %v", err)
	}
	defer closeClipboard.Call()
	if ok, _, err := empty.Call(); ok == 0 {
		return fmt.Errorf("EmptyClipboard: %v", err)
	}
	if ok, _, err := set.Call(unicodeText, handle); ok == 0 {
		return fmt.Errorf("SetClipboardData: %v", err)
	}
	owned = false // Ownership passes to Windows.
	return nil
}

type message struct {
	hwnd           uintptr
	kind           uint32
	wparam, lparam uintptr
	time           uint32
	x, y           int32
	private        uint32
}

// Watch blocks on WM_CLIPBOARDUPDATE. There is no clipboard polling timer.
func Watch(ctx context.Context, changed func()) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, err := newWindow()
	if err != nil {
		return err
	}
	defer destroyWindow.Call(hwnd)
	if ok, _, err := addListener.Call(hwnd); ok == 0 {
		return fmt.Errorf("AddClipboardFormatListener: %v", err)
	}
	defer removeListener.Call(hwnd)
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			post.Call(hwnd, 0x8001, 0, 0)
		case <-done:
		}
	}()
	defer func() { close(done); <-joined }()
	for {
		var msg message
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("GetMessageW: %v", err)
		}
		if result == 0 || msg.kind == 0x8001 {
			return nil
		}
		if msg.kind == 0x031d {
			changed()
		}
		dispatch.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
