package clipsync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClipboard struct {
	mu   sync.Mutex
	text string
	fail bool
}

func (c *fakeClipboard) Read() (string, error) { c.mu.Lock(); defer c.mu.Unlock(); return c.text, nil }
func (c *fakeClipboard) Write(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errors.New("busy")
	}
	c.text = text
	return nil
}
func request(s *Server, method, path, text, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(text))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAuthAndLimits(t *testing.T) {
	s := New("secret", nil)
	for _, tc := range []struct {
		key, text string
		want      int
	}{
		{"wrong", "hello", 401}, {"secret", "", 400}, {"secret", "a\x00b", 400},
		{"secret", string([]byte{0xff}), 400}, {"secret", strings.Repeat("x", MaxText+1), 413},
		{"secret", "buffersync://private?key=abc", 400}, {"secret", "Привет 👋\nвторая строка", 204},
	} {
		if got := request(s, "POST", "/clipboard", tc.text, tc.key).Code; got != tc.want {
			t.Fatalf("got %d, want %d", got, tc.want)
		}
	}
}
func TestReceiveWriteAndNoEcho(t *testing.T) {
	c := &fakeClipboard{}
	s := New("secret", c)
	if got := request(s, "POST", "/clipboard", "Привет 👋", "secret").Code; got != 204 {
		t.Fatal(got)
	}
	if c.text != "Привет 👋" {
		t.Fatal("not written to OS")
	}
	if err := s.Capture(); err != nil {
		t.Fatal(err)
	}
	if got := request(s, "POST", "/clipboard", "Привет 👋", "secret").Code; got != 204 {
		t.Fatal(got)
	}
	response := request(s, "GET", "/clipboard?after=0", "", "secret")
	if response.Header().Get("X-Revision") != "1" || response.Body.String() != "Привет 👋" {
		t.Fatal("echo changed revision or text")
	}
	c.text = "buffersync://do-not-broadcast"
	_ = s.Capture()
	if s.revision != 1 {
		t.Fatal("pairing secret leaked")
	}
}
func TestFailedOSWriteDoesNotPublish(t *testing.T) {
	s := New("secret", &fakeClipboard{fail: true})
	if got := request(s, "POST", "/clipboard", "hello", "secret").Code; got != 503 {
		t.Fatal(got)
	}
	if s.revision != 0 {
		t.Fatal("failed write was announced as success")
	}
}
func TestEventWakesMultipleReceivers(t *testing.T) {
	s := New("secret", nil)
	server := httptest.NewTLSServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/clipboard?after=0", nil)
			req.Header.Set("Authorization", "Bearer secret")
			response, err := server.Client().Do(req)
			if err != nil {
				results <- err.Error()
				return
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			results <- string(body)
		}()
	}
	if got := request(s, "POST", "/clipboard", "shared text", "secret").Code; got != 204 {
		t.Fatal(got)
	}
	for i := 0; i < 2; i++ {
		if got := <-results; got != "shared text" {
			t.Fatal(got)
		}
	}
}
func TestCanceledWaitAndProbe(t *testing.T) {
	s := New("secret", nil)
	if got := request(s, "GET", "/clipboard?check=1", "", "secret").Code; got != 204 {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/clipboard?after=0", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
