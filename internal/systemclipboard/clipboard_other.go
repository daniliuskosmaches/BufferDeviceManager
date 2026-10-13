//go:build !windows && !darwin

package systemclipboard

import (
	"context"
	"fmt"
)

type Clipboard struct{}

func New() (*Clipboard, error) {
	return nil, fmt.Errorf("системный буфер поддержан на Windows и macOS; для обмена между телефонами используйте -memory")
}
func (*Clipboard) Read() (string, error)  { return "", fmt.Errorf("unsupported platform") }
func (*Clipboard) Write(string) error     { return fmt.Errorf("unsupported platform") }
func Watch(context.Context, func()) error { return fmt.Errorf("unsupported platform") }
