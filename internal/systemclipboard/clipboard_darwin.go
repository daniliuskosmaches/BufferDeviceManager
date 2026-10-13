//go:build darwin

package systemclipboard

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Clipboard struct{}

func New() (*Clipboard, error) {
	for _, tool := range []string{"pbcopy", "pbpaste"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%s: %w", tool, err)
		}
	}
	return &Clipboard{}, nil
}
func (c *Clipboard) Read() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "pbpaste").Output()
	return string(data), err
}
func (c *Clipboard) Write(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// The optional macOS CLI fallback polls once per second. Android and Windows use events.
func Watch(ctx context.Context, changed func()) error {
	c := &Clipboard{}
	previous, err := c.Read()
	if err != nil {
		return err
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			current, err := c.Read()
			if err != nil {
				return err
			}
			if current != previous {
				previous = current
				changed()
			}
		}
	}
}
