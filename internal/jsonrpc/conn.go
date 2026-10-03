package jsonrpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Handler processes one incoming message. Returning stop=true ends Serve
// cleanly; this is how the LSP exit notification terminates the loop without
// waiting for the client to close the stream.
type Handler func(m *Message) (stop bool)

// Conn pairs a framed reader with a writer that may be shared by several
// goroutines. Reading is single-threaded by design: only Serve or Read may be
// used, from one goroutine.
type Conn struct {
	r  *bufio.Reader
	mu sync.Mutex
	w  io.Writer
}

// NewConn wraps a byte stream pair.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{r: bufio.NewReader(r), w: w}
}

// Read returns the next message from the stream; see the package-level Read.
func (c *Conn) Read() (*Message, error) {
	return Read(c.r)
}

// Send writes one framed message. Safe for concurrent use.
func (c *Conn) Send(m *Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Write(c.w, m)
}

// Reply sends a successful response. A nil result, including a typed nil
// pointer, is sent as "result":null.
func (c *Conn) Reply(id *json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return c.Send(&Message{JSONRPC: Version, ID: id, Result: raw})
}

// ReplyError sends an error response. A nil id is sent as "id":null, which
// JSON-RPC requires when the request id could not be determined.
func (c *Conn) ReplyError(id *json.RawMessage, e *Error) error {
	if id == nil {
		null := json.RawMessage("null")
		id = &null
	}
	return c.Send(&Message{JSONRPC: Version, ID: id, Error: e})
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params for %s: %w", method, err)
	}
	return c.Send(&Message{JSONRPC: Version, Method: method, Params: raw})
}

// Serve reads messages and hands them to h until h asks to stop or the stream
// ends. A body that is not valid JSON is answered with ParseError and
// skipped, since the framing is still intact. It returns nil when h stopped
// the loop, io.EOF when the peer closed the stream between messages, and a
// wrapped error otherwise.
func (c *Conn) Serve(h Handler) error {
	for {
		m, err := c.Read()
		if err != nil {
			var de *DecodeError
			if errors.As(err, &de) {
				if rerr := c.ReplyError(nil, NewError(ParseError, "%v", de.Err)); rerr != nil {
					return rerr
				}
				continue
			}
			return err
		}
		if h(m) {
			return nil
		}
	}
}
