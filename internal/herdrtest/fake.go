// Package herdrtest provides a stand-in for the Herdr socket.
//
// It lives in its own package rather than a _test.go file so that every layer
// above the client -- the watcher, the commands -- can be tested against the
// same wire format, instead of only the client that talks to it.
package herdrtest

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Request is one decoded call from the client.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Fake speaks Herdr's newline-delimited JSON over a unix socket.
type Fake struct {
	t    *testing.T
	ln   net.Listener
	Path string

	mu       sync.Mutex
	requests []Request
	handler  func(Request) any
	closed   bool

	// Stream carries lines written to a subscriber after the ack.
	Stream chan string
}

// Start listens on a temporary socket and points HERDR_SOCKET_PATH at it.
func Start(t *testing.T) *Fake {
	t.Helper()

	// macOS caps a unix socket path near 104 bytes, and t.TempDir() embeds the
	// test name -- long names would exceed it. A short directory keeps every
	// test able to listen rather than skipping, which would look like a pass.
	dir, err := os.MkdirTemp("", "hb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("could not listen on %s: %v", path, err)
	}

	f := &Fake{t: t, ln: ln, Path: path, Stream: make(chan string, 16)}
	t.Setenv("HERDR_SOCKET_PATH", path)
	t.Cleanup(f.Close)

	go f.serve()
	return f
}

// Close stops listening, which is how "Herdr went away" is simulated.
func (f *Fake) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	_ = f.ln.Close()
}

func (f *Fake) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *Fake) handle(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return
		}

		f.mu.Lock()
		f.requests = append(f.requests, req)
		handler := f.handler
		f.mu.Unlock()

		if req.Method == "events.subscribe" {
			f.serveStream(conn, req)
			return
		}
		if handler == nil {
			continue // no reply, so the caller waits or times out
		}
		reply := handler(req)
		if reply == nil {
			continue
		}
		body, err := json.Marshal(reply)
		if err != nil {
			return
		}
		if _, err := conn.Write(append(body, '\n')); err != nil {
			return
		}
	}
}

// serveStream acks the subscription and then streams, which is the shape the
// real server uses.
func (f *Fake) serveStream(conn net.Conn, req Request) {
	ack, _ := json.Marshal(map[string]any{
		"id":     req.ID,
		"result": map[string]any{"type": "subscription_started"},
	})
	if _, err := conn.Write(append(ack, '\n')); err != nil {
		return
	}
	for line := range f.Stream {
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			return
		}
	}
}

// Handle installs a per-method responder.
func (f *Fake) Handle(handler func(Request) any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

// OK answers every method with one result.
func (f *Fake) OK(result any) {
	f.Handle(func(req Request) any {
		return map[string]any{"id": req.ID, "result": result}
	})
}

// Route answers per method, falling back to a plain ok.
func (f *Fake) Route(routes map[string]any) {
	f.Handle(func(req Request) any {
		if result, ok := routes[req.Method]; ok {
			return map[string]any{"id": req.ID, "result": result}
		}
		return map[string]any{"id": req.ID, "result": map[string]any{"type": "ok"}}
	})
}

// Fail answers every method with an error.
func (f *Fake) Fail(code, message string) {
	f.Handle(func(req Request) any {
		return map[string]any{
			"id":    req.ID,
			"error": map[string]string{"code": code, "message": message},
		}
	})
}

// Requests returns everything received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Called reports how many times a method was invoked.
func (f *Fake) Called(method string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// Last returns the most recent call to a method.
func (f *Fake) Last(t *testing.T, method string) Request {
	t.Helper()
	reqs := f.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method {
			return reqs[i]
		}
	}
	t.Fatalf("%s was never called; saw %v", method, methods(reqs))
	return Request{}
}

// LastAny returns the most recent call, whatever its method.
func (f *Fake) LastAny(t *testing.T) Request {
	t.Helper()
	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("the client sent nothing")
	}
	return reqs[len(reqs)-1]
}

// Params decodes a request's parameters.
func Params(t *testing.T, req Request) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(req.Params, &out); err != nil {
		t.Fatalf("params were not an object: %v", err)
	}
	return out
}

// WaitFor polls until cond holds, so tests do not race the server goroutine.
func WaitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func methods(reqs []Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Method)
	}
	return out
}
