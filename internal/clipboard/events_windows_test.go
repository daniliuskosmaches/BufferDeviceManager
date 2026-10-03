//go:build windows

package main

import (
	"context"
	"errors"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// These tests run on Windows without reading or changing the user's clipboard.
func TestNativeDelegateLifetimeAndSignal(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d, err := newDelegate(iidHistoryHandler)
	if err != nil {
		t.Fatal(err)
	}
	defer delegateRelease(d.address)

	var queried uintptr
	hr := delegateQuery(d.address, uintptr(unsafe.Pointer(&iidHistoryHandler)), uintptr(unsafe.Pointer(&queried)))
	if hr != 0 || queried != d.address {
		t.Fatalf("QueryInterface = %#x, %#x", hr, queried)
	}
	if refs := delegateRelease(queried); refs != 1 {
		t.Fatalf("owner reference lost: %d", refs)
	}
	unknownIID := syscallGUIDForTest()
	queried = 123
	hr = delegateQuery(d.address, uintptr(unsafe.Pointer(&unknownIID)), uintptr(unsafe.Pointer(&queried)))
	if hr != 0x80004002 || queried != 0 {
		t.Fatalf("unsupported IID: %#x, %#x", hr, queried)
	}

	// Notification before waiting must remain signaled, not be lost.
	delegateInvoke(d.address, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := waitSignals(ctx, d.signal); err != nil {
		t.Fatal(err)
	}
	// Auto-reset signal is consumed; a fresh wait must end by cancellation.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := waitSignals(ctx2, d.signal); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func syscallGUIDForTest() syscall.GUID { return syscall.GUID{Data1: 0x12345678} }

func TestCancellationWakesNativeWait(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d, err := newDelegate(iidHistoryCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer delegateRelease(d.address)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := waitSignals(ctx, d.signal); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}
