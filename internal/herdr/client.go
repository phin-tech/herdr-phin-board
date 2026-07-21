// Package herdr is a minimal client for the Herdr socket API.
//
// The socket speaks newline-delimited JSON: write {"id","method","params"},
// read back {"id","result"} or {"id","error"}. A connection that has issued
// events.subscribe becomes a one-way stream, so subscriptions get their own
// connection and plain requests each use a short-lived one.
package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"time"
)

// newLineScanner reads newline-delimited JSON. Session snapshots run well past
// bufio.Scanner's 64KiB default, so the buffer is raised.
func newLineScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 8<<20)
	return s
}

// Client talks to the Herdr server over its unix socket.
type Client struct {
	socketPath string
	seq        atomic.Uint64
}

// New builds a client from HERDR_SOCKET_PATH.
func New() (*Client, error) {
	path := os.Getenv("HERDR_SOCKET_PATH")
	if path == "" {
		return nil, errors.New("HERDR_SOCKET_PATH is not set: run this inside a Herdr session")
	}
	return &Client{socketPath: path}, nil
}

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *apiError       `json:"error"`
}

func (c *Client) nextID(method string) string {
	return fmt.Sprintf("board:%s:%d", method, c.seq.Add(1))
}

// Request performs one request/response round trip.
func (c *Client) Request(method string, params any, out any) error {
	conn, err := net.DialTimeout("unix", c.socketPath, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial herdr socket: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	payload, err := json.Marshal(request{ID: c.nextID(method), Method: method, Params: params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", method, err)
	}

	// Responses can exceed bufio's default buffer on large snapshots.
	reader := bufio.NewReaderSize(conn, 1<<20)
	scanner := newLineScanner(reader)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read %s: %w", method, err)
		}
		return fmt.Errorf("read %s: connection closed with no response", method)
	}

	var resp response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("decode %s: %w", method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s (%s)", method, resp.Error.Message, resp.Error.Code)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// Event is one streamed subscription event.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// WorkspaceSubscriptions are the workspace-scoped event types the board cares
// about. Pane-scoped subscriptions are deliberately excluded: they require a
// pane_id, and agent activity is not what this board tracks.
var WorkspaceSubscriptions = []string{
	"workspace.created",
	"workspace.updated",
	"workspace.renamed",
	"workspace.closed",
	"workspace.focused",
	"workspace.metadata_updated",
}

// Subscribe streams events until ctx is cancelled or the connection drops.
// It holds its own connection for the lifetime of the stream.
func (c *Client) Subscribe(ctx context.Context, types []string, out chan<- Event) error {
	conn, err := net.DialTimeout("unix", c.socketPath, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial herdr socket: %w", err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	subs := make([]map[string]string, 0, len(types))
	for _, t := range types {
		subs = append(subs, map[string]string{"type": t})
	}
	payload, err := json.Marshal(request{
		ID:     c.nextID("events.subscribe"),
		Method: "events.subscribe",
		Params: map[string]any{"subscriptions": subs},
	})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	scanner := newLineScanner(bufio.NewReaderSize(conn, 1<<20))
	for scanner.Scan() {
		line := scanner.Bytes()

		// The first line is the subscription ack; after that it is all events.
		var resp response
		if err := json.Unmarshal(line, &resp); err == nil && resp.Error != nil {
			return fmt.Errorf("subscribe: %s", resp.Error.Message)
		}

		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}
