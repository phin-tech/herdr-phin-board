package herdr

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

// fakeHerdr is a stand-in for the Herdr socket: it speaks the same
// newline-delimited JSON, so the client can be tested without a session.
//
// This layer was previously covered only by running against a live Herdr. That
// caught the integration bugs, but left every parsing path -- envelopes,
// errors, the subscription's ack-then-stream shape -- with nothing to fail
// when it breaks.
type fakeHerdr struct {
	t        *testing.T
	listener net.Listener
	path     string

	mu       sync.Mutex
	requests []request
	// handler answers a method. Returning nil means "no reply", which is how
	// a hung server is simulated.
	handler func(req request) any
	// stream is written to every subscriber after the ack, one event per line.
	stream chan string
	closed bool
}

func newFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()

	// macOS caps a unix socket path near 104 bytes, and t.TempDir() embeds the
	// test name -- long names would silently exceed it. A short directory of
	// our own keeps every test able to listen, rather than skipping the ones
	// whose names happen to be long.
	dir, err := os.MkdirTemp("", "hb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "h.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("could not listen on %s: %v", path, err)
	}

	f := &fakeHerdr{t: t, listener: l, path: path, stream: make(chan string, 16)}
	t.Setenv("HERDR_SOCKET_PATH", path)
	t.Cleanup(f.Close)

	go f.serve()
	return f
}

func (f *fakeHerdr) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	_ = f.listener.Close()
}

func (f *fakeHerdr) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeHerdr) handle(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	for scanner.Scan() {
		var req request
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
			continue // no reply: the client should time out or block
		}
		reply := handler(req)
		if reply == nil {
			continue
		}
		body, err := json.Marshal(reply)
		if err != nil {
			f.t.Errorf("fake could not encode a reply: %v", err)
			return
		}
		if _, err := conn.Write(append(body, '\n')); err != nil {
			return
		}
	}
}

// serveStream sends the subscription ack and then whatever is queued, which is
// the shape the real server uses.
func (f *fakeHerdr) serveStream(conn net.Conn, req request) {
	ack, _ := json.Marshal(map[string]any{
		"id":     req.ID,
		"result": map[string]any{"type": "subscription_started"},
	})
	if _, err := conn.Write(append(ack, '\n')); err != nil {
		return
	}

	for line := range f.stream {
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			return
		}
	}
}

func (f *fakeHerdr) reply(handler func(req request) any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

// ok answers every method with a fixed result.
func (f *fakeHerdr) ok(result any) {
	f.reply(func(req request) any {
		return map[string]any{"id": req.ID, "result": result}
	})
}

func (f *fakeHerdr) fail(code, message string) {
	f.reply(func(req request) any {
		return map[string]any{
			"id":    req.ID,
			"error": map[string]string{"code": code, "message": message},
		}
	})
}

func (f *fakeHerdr) seen() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

func (f *fakeHerdr) lastRequest(t *testing.T) request {
	t.Helper()
	reqs := f.seen()
	if len(reqs) == 0 {
		t.Fatal("the client sent nothing")
	}
	return reqs[len(reqs)-1]
}

// waitFor polls until cond holds, so tests do not race the server goroutine.
func waitFor(t *testing.T, why string, cond func() bool) {
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
