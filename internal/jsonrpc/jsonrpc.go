// Package jsonrpc implements the JSON-RPC 2.0 framing used by the Language
// Server Protocol: a Content-Length header block followed by a JSON body.
// It is deliberately small; the LSP needs only one transport and the
// project allows no third-party modules.
package jsonrpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"
)

// Version is the only protocol version JSON-RPC 2.0 peers accept.
const Version = "2.0"

// Standard JSON-RPC and LSP error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603

	// LSP-reserved codes.
	ServerNotInitialized = -32002
	UnknownErrorCode     = -32001
	RequestFailed        = -32803
	ServerCancelled      = -32802
	ContentModified      = -32801
	RequestCancelled     = -32800
)

// maxBody bounds a single message so a corrupt or hostile Content-Length
// cannot make the server allocate without limit. Assembly sources are small;
// 64 MiB leaves ample room for a full-document sync.
const maxBody = 64 << 20

// Message is the union of request, notification and response. Which one it
// is follows from which fields are set; see IsRequest and friends.
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Error is the JSON-RPC error object. It satisfies the error interface so a
// handler can return one directly and have its code preserved.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// NewError builds an Error with a formatted message.
func NewError(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsRequest reports whether m expects a response.
func (m *Message) IsRequest() bool { return m.Method != "" && m.ID != nil }

// IsNotification reports whether m is a method call without an id.
func (m *Message) IsNotification() bool { return m.Method != "" && m.ID == nil }

// IsResponse reports whether m answers an earlier request.
func (m *Message) IsResponse() bool { return m.Method == "" && m.ID != nil }

// MarshalJSON exists because omitempty would drop a null result, and a
// response carrying neither result nor error is rejected by clients. A hover
// with nothing to show is exactly such a null result.
func (m *Message) MarshalJSON() ([]byte, error) {
	type plain Message
	out := plain(*m)
	if out.JSONRPC == "" {
		out.JSONRPC = Version
	}
	if out.Method == "" && out.Error == nil && len(out.Result) == 0 {
		out.Result = json.RawMessage("null")
	}
	return json.Marshal(&out)
}

// DecodeError reports a frame whose header was well formed but whose body
// was not valid JSON. The stream is still in sync, so the caller can answer
// with ParseError and keep reading.
type DecodeError struct {
	Err error
}

func (e *DecodeError) Error() string { return fmt.Sprintf("decode message body: %v", e.Err) }

func (e *DecodeError) Unwrap() error { return e.Err }

// Read returns the next message. It returns io.EOF only when the stream ends
// cleanly between messages; an end of stream inside a frame is
// io.ErrUnexpectedEOF. A *DecodeError means the frame was consumed and the
// stream may continue; any other error means framing is lost.
func Read(r *bufio.Reader) (*Message, error) {
	tp := textproto.NewReader(r)
	hdr, err := tp.ReadMIMEHeader()
	if err != nil {
		// ReadMIMEHeader returns io.EOF only if nothing at all was read.
		if errors.Is(err, io.EOF) && len(hdr) == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read header: %w", io.ErrUnexpectedEOF)
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	raw := hdr.Get("Content-Length")
	if raw == "" {
		return nil, errors.New("read header: missing Content-Length")
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("read header: bad Content-Length %q: %w", raw, err)
	}
	if n < 0 || n > maxBody {
		return nil, fmt.Errorf("read header: Content-Length %d out of range", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("read body: %w", err)
	}
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, &DecodeError{Err: err}
	}
	return &m, nil
}

// Write frames and writes m in a single Write call so that, behind a mutex,
// concurrent writers never interleave partial frames.
func Write(w io.Writer, m *Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	return writeFrame(w, body)
}

// writeFrame prefixes body with its header and writes both in one call.
func writeFrame(w io.Writer, body []byte) error {
	frame := make([]byte, 0, len(body)+32)
	frame = fmt.Appendf(frame, "Content-Length: %d\r\n\r\n", len(body))
	frame = append(frame, body...)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}
