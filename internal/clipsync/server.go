// Package clipsync implements a small, versioned text clipboard shared over HTTPS.
package clipsync

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxText = 64 * 1024

// Clipboard must only return plain text. An empty value is ignored, not a deletion.
type Clipboard interface {
	Read() (string, error)
	Write(string) error
}

type Server struct {
	mu        sync.Mutex
	key       string
	clipboard Clipboard
	text      string
	revision  uint64
	changed   chan struct{}
}

func New(key string, clipboard Clipboard) *Server {
	return &Server{key: key, clipboard: clipboard, changed: make(chan struct{})}
}

func ValidText(s string) bool {
	return s != "" && len(s) <= MaxText && utf8.ValidString(s) && !strings.ContainsRune(s, '\x00')
}

func (s *Server) publishLocked(text string) {
	if text == s.text {
		return
	}
	s.text = text
	s.revision++
	close(s.changed)
	s.changed = make(chan struct{})
}

// Capture serializes the OS read with incoming writes, preventing stale echoes.
func (s *Server) Capture() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clipboard == nil {
		return nil
	}
	text, err := s.clipboard.Read()
	if err != nil {
		return err
	}
	// Never broadcast the secret pairing link printed by the companion.
	if ValidText(text) && !strings.HasPrefix(text, "buffersync://") {
		s.publishLocked(text)
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(s.key)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/clipboard" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxText))
		if err != nil {
			http.Error(w, "text too large", http.StatusRequestEntityTooLarge)
			return
		}
		text := string(body)
		if !ValidText(text) || strings.HasPrefix(text, "buffersync://") {
			http.Error(w, "invalid text", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		// Report success only after the desktop clipboard has actually accepted it.
		if s.clipboard != nil {
			if err := s.clipboard.Write(text); err != nil {
				s.mu.Unlock()
				http.Error(w, "desktop clipboard unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		s.publishLocked(text)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		if r.URL.Query().Get("check") == "1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		if err != nil {
			http.Error(w, "invalid revision", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		for {
			s.mu.Lock()
			revision, text, changed := s.revision, s.text, s.changed
			s.mu.Unlock()
			if revision > after {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("X-Revision", strconv.FormatUint(revision, 10))
				fmt.Fprint(w, text)
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
