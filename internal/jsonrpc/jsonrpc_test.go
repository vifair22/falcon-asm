package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func frame(body string) string {
	return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

func rawID(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func TestRead(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantMethod string
		wantID     string
		wantErr    error // matched with errors.Is when set
		wantDecode bool
		wantAnyErr bool
	}{
		{name: "request", in: frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`), wantMethod: "initialize", wantID: "1"},
		{name: "string id", in: frame(`{"jsonrpc":"2.0","id":"a","method":"x"}`), wantMethod: "x", wantID: `"a"`},
		{name: "content type header and odd case", in: "content-length: 30\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n" + `{"jsonrpc":"2.0","method":"n"}`, wantMethod: "n"},
		{name: "clean eof", in: "", wantErr: io.EOF},
		{name: "eof in header", in: "Content-Length: 5\r\n", wantErr: io.ErrUnexpectedEOF},
		{name: "eof in body", in: "Content-Length: 50\r\n\r\n{}", wantErr: io.ErrUnexpectedEOF},
		{name: "missing length", in: "Content-Type: x\r\n\r\n{}", wantAnyErr: true},
		{name: "bad length", in: "Content-Length: abc\r\n\r\n{}", wantAnyErr: true},
		{name: "negative length", in: "Content-Length: -1\r\n\r\n{}", wantAnyErr: true},
		{name: "huge length", in: "Content-Length: 999999999999\r\n\r\n{}", wantAnyErr: true},
		{name: "malformed header line", in: "garbage\r\n\r\n{}", wantAnyErr: true},
		{name: "bad json", in: frame(`{"jsonrpc":`), wantDecode: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Read(bufio.NewReader(strings.NewReader(tt.in)))
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if errors.Is(tt.wantErr, io.EOF) && err.Error() != io.EOF.Error() {
					t.Fatalf("clean EOF was wrapped: %v", err)
				}
				return
			case tt.wantDecode:
				var de *DecodeError
				if !errors.As(err, &de) {
					t.Fatalf("err = %v, want DecodeError", err)
				}
				if de.Unwrap() == nil || de.Error() == "" {
					t.Fatalf("DecodeError lacks cause")
				}
				return
			case tt.wantAnyErr:
				if err == nil {
					t.Fatalf("expected error")
				}
				var de *DecodeError
				if errors.As(err, &de) {
					t.Fatalf("framing error reported as DecodeError: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.Method != tt.wantMethod {
				t.Errorf("method = %q, want %q", m.Method, tt.wantMethod)
			}
			gotID := ""
			if m.ID != nil {
				gotID = string(*m.ID)
			}
			if gotID != tt.wantID {
				t.Errorf("id = %q, want %q", gotID, tt.wantID)
			}
		})
	}
}

func TestReadSequence(t *testing.T) {
	in := frame(`{"jsonrpc":"2.0","method":"a"}`) + frame(`{"jsonrpc":"2.0","method":"b"}`)
	r := bufio.NewReader(strings.NewReader(in))
	for _, want := range []string{"a", "b"} {
		m, err := Read(r)
		if err != nil {
			t.Fatal(err)
		}
		if m.Method != want {
			t.Fatalf("method = %q, want %q", m.Method, want)
		}
	}
	if _, err := Read(r); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestWrite(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
		want string
	}{
		{
			name: "null result is kept",
			msg:  &Message{ID: rawID("1")},
			want: `{"jsonrpc":"2.0","id":1,"result":null}`,
		},
		{
			name: "explicit result",
			msg:  &Message{JSONRPC: Version, ID: rawID("2"), Result: json.RawMessage(`{"a":1}`)},
			want: `{"jsonrpc":"2.0","id":2,"result":{"a":1}}`,
		},
		{
			name: "error response has no result",
			msg:  &Message{ID: rawID("3"), Error: &Error{Code: MethodNotFound, Message: "nope"}},
			want: `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"nope"}}`,
		},
		{
			name: "notification has no result",
			msg:  &Message{Method: "window/logMessage", Params: json.RawMessage(`{}`)},
			want: `{"jsonrpc":"2.0","method":"window/logMessage","params":{}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Write(&buf, tt.msg); err != nil {
				t.Fatal(err)
			}
			if got, want := buf.String(), frame(tt.want); got != want {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
			// Round trip through Read to prove the framing is self-consistent.
			m, err := Read(bufio.NewReader(&buf))
			if err != nil {
				t.Fatal(err)
			}
			if (m.ID == nil) != (tt.msg.ID == nil) {
				t.Fatalf("id presence changed in round trip")
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestWriteErrors(t *testing.T) {
	if err := Write(failWriter{}, &Message{Method: "x"}); err == nil {
		t.Fatal("expected write error")
	}
	bad := &Message{ID: rawID("1"), Result: json.RawMessage(`{not json`)}
	if err := Write(io.Discard, bad); err == nil {
		t.Fatal("expected encode error")
	}
}

func TestMessageKinds(t *testing.T) {
	tests := []struct {
		name               string
		m                  Message
		req, notif, respns bool
	}{
		{"request", Message{ID: rawID("1"), Method: "m"}, true, false, false},
		{"notification", Message{Method: "m"}, false, true, false},
		{"response", Message{ID: rawID("1")}, false, false, true},
		{"empty", Message{}, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.IsRequest(); got != tt.req {
				t.Errorf("IsRequest = %v", got)
			}
			if got := tt.m.IsNotification(); got != tt.notif {
				t.Errorf("IsNotification = %v", got)
			}
			if got := tt.m.IsResponse(); got != tt.respns {
				t.Errorf("IsResponse = %v", got)
			}
		})
	}
}

func TestErrorString(t *testing.T) {
	e := NewError(InvalidParams, "bad %s", "thing")
	if e.Code != InvalidParams || e.Message != "bad thing" {
		t.Fatalf("NewError = %+v", e)
	}
	if !strings.Contains(e.Error(), "-32602") || !strings.Contains(e.Error(), "bad thing") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

func TestConnHelpers(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(strings.NewReader(""), &buf)
	var nilPtr *struct{ A int }
	if err := c.Reply(rawID("1"), nilPtr); err != nil {
		t.Fatal(err)
	}
	if err := c.Reply(rawID("2"), map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReplyError(nil, NewError(ParseError, "x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Notify("n", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reply(rawID("3"), func() {}); err == nil {
		t.Fatal("expected encode error for func result")
	}
	if err := c.Notify("n", make(chan int)); err == nil {
		t.Fatal("expected encode error for chan params")
	}

	want := []string{
		`{"jsonrpc":"2.0","id":1,"result":null}`,
		`{"jsonrpc":"2.0","id":2,"result":{"a":1}}`,
		`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"x"}}`,
		`{"jsonrpc":"2.0","method":"n","params":{"k":"v"}}`,
	}
	// Compare raw bodies: decoding would turn "id":null back into a nil
	// pointer and hide whether it was on the wire.
	frames := strings.Split(buf.String(), "Content-Length: ")[1:]
	if len(frames) != len(want) {
		t.Fatalf("got %d frames, want %d", len(frames), len(want))
	}
	for i, w := range want {
		_, body, ok := strings.Cut(frames[i], "\r\n\r\n")
		if !ok || body != w {
			t.Errorf("message %d = %q, want %q", i, body, w)
		}
	}
}

func TestConnSendConcurrent(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(strings.NewReader(""), &buf)
	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Notify("ping", map[string]string{"pad": strings.Repeat("x", 100)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r := bufio.NewReader(&buf)
	for i := range n {
		m, err := Read(r)
		if err != nil {
			t.Fatalf("frame %d corrupted: %v", i, err)
		}
		if m.Method != "ping" {
			t.Fatalf("frame %d method %q", i, m.Method)
		}
	}
}

func TestServe(t *testing.T) {
	in := frame(`{"jsonrpc":"2.0","method":"a"}`) +
		frame(`{oops`) +
		frame(`{"jsonrpc":"2.0","method":"stop"}`) +
		frame(`{"jsonrpc":"2.0","method":"never"}`)
	var out bytes.Buffer
	c := NewConn(strings.NewReader(in), &out)
	var seen []string
	err := c.Serve(func(m *Message) bool {
		seen = append(seen, m.Method)
		return m.Method == "stop"
	})
	if err != nil {
		t.Fatalf("Serve = %v, want nil", err)
	}
	if strings.Join(seen, ",") != "a,stop" {
		t.Fatalf("seen = %v", seen)
	}
	m, err := Read(bufio.NewReader(&out))
	if err != nil {
		t.Fatal(err)
	}
	if m.Error == nil || m.Error.Code != ParseError {
		t.Fatalf("expected ParseError reply, got %+v", m)
	}
}

func TestServeEndings(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		out   io.Writer
		check func(error) bool
	}{
		{"eof", frame(`{"jsonrpc":"2.0","method":"a"}`), io.Discard, func(err error) bool { return errors.Is(err, io.EOF) }},
		{"framing", "junk\r\n\r\n", io.Discard, func(err error) bool { return err != nil && !errors.Is(err, io.EOF) }},
		{"reply fails", frame(`{bad`), failWriter{}, func(err error) bool { return err != nil && !errors.Is(err, io.EOF) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConn(strings.NewReader(tt.in), tt.out)
			err := c.Serve(func(*Message) bool { return false })
			if !tt.check(err) {
				t.Fatalf("Serve = %v", err)
			}
		})
	}
}
