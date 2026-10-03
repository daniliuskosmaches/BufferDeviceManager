//go:build windows
// +build windows

package main

import (
	"BufferSync/internal/historydiff"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := watchHistory(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func watchHistory(ctx context.Context) (watchErr error) {
	// WinRT initialization and all interface calls share one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	reader, err := newHistoryReader()
	if err != nil {
		return err
	}
	defer reader.close()

	controller, err := startDispatcher()
	if err != nil {
		return err
	}
	defer func() {
		if err := stopDispatcher(controller); err != nil && watchErr == nil {
			watchErr = err
		}
	}()
	subscription, err := reader.subscribe()
	if err != nil {
		return fmt.Errorf("подписка на историю: %w", err)
	}
	defer func() {
		if err := subscription.close(); err != nil && watchErr == nil {
			watchErr = err
		}
	}()

	fmt.Println("Наблюдение за историей Win+V. Выход: Ctrl+C.")
	var previous []string
	initialized := false
	lastError := ""
	for {
		current, err := reader.read(ctx)
		if ctx.Err() != nil {
			fmt.Println("Отслеживание остановлено.")
			return nil
		}
		if err != nil {
			if err.Error() != lastError {
				fmt.Fprintln(os.Stderr, err)
				lastError = err.Error()
			}
			// Disabled history is not an empty snapshot. After re-enabling,
			// establish a fresh baseline; do not invent deletion events.
			if errors.Is(err, errHistoryDisabled) {
				initialized = false
			}
		} else {
			lastError = ""
			if initialized {
				added, removed := historydiff.Changes(previous, current)
				for range added {
					fmt.Println("добавление")
				}
				for range removed {
					fmt.Println("удаление")
				}
			} else {
				fmt.Printf("История доступна: %d элементов. Ожидание изменений...\n", len(current))
			}
			previous = current
			initialized = true
		}
		// Subscribe before the baseline read so notifications during a read
		// remain signaled. Repeated notifications can safely share one refresh.
		_, err = waitSignals(ctx, subscription.history.signal, subscription.enabled.signal)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				fmt.Println("Отслеживание остановлено.")
				return nil
			}
			return err
		}
	}
}
