package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// response is one reply as the client saw it. at is taken as soon as the
// frame body is in memory and before it is decoded, so the client's own JSON
// work on a large reply is not charged to the server.
type response struct {
	result json.RawMessage
	err    *rpcError
	at     time.Time
	size   int
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// publish is one textDocument/publishDiagnostics as received. version is
// the document version it describes, -1 when the server sent none.
type publish struct {
	at      time.Time
	count   int
	version int
	diags   []json.RawMessage
}

// client drives one server process over its stdin/stdout. Requests may be
// pipelined: every id has its own reply channel, filled by the read loop.
type client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	spawned time.Time
	stderr  bytes.Buffer
	timeout time.Duration
	delta   bool // the server offers semanticTokens/full/delta

	wmu    sync.Mutex
	nextID int

	mu      sync.Mutex
	cond    *sync.Cond
	pending map[int]chan response
	diags   map[string][]publish
	readErr error
	done    chan struct{}
}

// errTimeout marks a reply or publish that did not arrive in time.
var errTimeout = errors.New("timed out")

// spawn starts the server and its read loop. The clock for cold-start
// metrics starts immediately before the process is created.
func spawn(bin string, args []string, timeout time.Duration) (*client, error) {
	c := &client{
		pending: map[int]chan response{},
		diags:   map[string][]publish{},
		done:    make(chan struct{}),
		timeout: timeout,
	}
	c.cond = sync.NewCond(&c.mu)
	c.cmd = exec.Command(bin, args...)
	c.cmd.Stderr = &lockedWriter{w: &c.stderr}
	in, err := c.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	out, err := c.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	c.stdin = in
	c.spawned = time.Now()
	if err := c.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go c.readLoop(bufio.NewReaderSize(out, 1<<20))
	return c, nil
}

// lockedWriter lets the exec copier goroutine and a failing scenario's error
// message read the server's stderr without racing.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (c *client) readLoop(r *bufio.Reader) {
	defer close(c.done)
	for {
		body, err := readFrame(r)
		at := time.Now()
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.cond.Broadcast()
			c.mu.Unlock()
			return
		}
		c.dispatch(body, at)
	}
}

// readFrame returns one Content-Length framed body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	hdr, err := textproto.NewReader(r).ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(hdr.Get("Content-Length")))
	if err != nil || n < 0 {
		return nil, fmt.Errorf("bad Content-Length %q", hdr.Get("Content-Length"))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

type incoming struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *client) dispatch(body []byte, at time.Time) {
	var m incoming
	if err := json.Unmarshal(body, &m); err != nil {
		return
	}
	if m.Method == "textDocument/publishDiagnostics" {
		var p struct {
			URI         string            `json:"uri"`
			Version     *int              `json:"version"`
			Diagnostics []json.RawMessage `json:"diagnostics"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			v := -1
			if p.Version != nil {
				v = *p.Version
			}
			c.mu.Lock()
			c.diags[p.URI] = append(c.diags[p.URI], publish{at: at, count: len(p.Diagnostics), version: v, diags: p.Diagnostics})
			c.cond.Broadcast()
			c.mu.Unlock()
		}
		return
	}
	if m.Method != "" || m.ID == nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[*m.ID]
	delete(c.pending, *m.ID)
	c.mu.Unlock()
	if ch != nil {
		ch <- response{result: m.Result, err: m.Error, at: at, size: len(body)}
	}
}

// write frames one message.
func (c *client) write(msg map[string]any) error {
	msg["jsonrpc"] = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	frame := make([]byte, 0, len(body)+32)
	frame = fmt.Appendf(frame, "Content-Length: %d\r\n\r\n", len(body))
	frame = append(frame, body...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.stdin.Write(frame); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// pendingReq is a request in flight.
type pendingReq struct {
	id   int
	sent time.Time
	ch   chan response
}

// send issues a request without waiting for it.
func (c *client) send(method string, params any) (*pendingReq, error) {
	c.wmu.Lock()
	c.nextID++
	id := c.nextID
	c.wmu.Unlock()
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	p := &pendingReq{id: id, ch: ch, sent: time.Now()}
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	return p, nil
}

// wait collects a pending request's reply and its latency.
func (c *client) wait(p *pendingReq) (response, time.Duration, error) {
	t := time.NewTimer(c.timeout)
	defer t.Stop()
	select {
	case r := <-p.ch:
		return r, r.at.Sub(p.sent), nil
	case <-c.done:
		// The reply may have been delivered just before the stream ended.
		select {
		case r := <-p.ch:
			return r, r.at.Sub(p.sent), nil
		default:
		}
		return response{}, 0, fmt.Errorf("request %d: server stream ended: %w; stderr:\n%s", p.id, c.readErrSafe(), c.stderrText())
	case <-t.C:
		return response{}, 0, fmt.Errorf("request %d: %w after %v", p.id, errTimeout, c.timeout)
	}
}

// call is send plus wait. A JSON-RPC error reply is returned as a response,
// not an error, so callers can count cancellations.
func (c *client) call(method string, params any) (response, time.Duration, error) {
	p, err := c.send(method, params)
	if err != nil {
		return response{}, 0, err
	}
	r, d, err := c.wait(p)
	if err != nil {
		return r, d, fmt.Errorf("%s: %w", method, err)
	}
	return r, d, nil
}

func (c *client) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *client) readErrSafe() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

func (c *client) stderrText() string {
	lw := c.cmd.Stderr.(*lockedWriter)
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.String()
}

// waitPublish blocks until uri gets a publish after t, or the timeout.
func (c *client) waitPublish(uri string, t time.Time, timeout time.Duration) (publish, error) {
	deadline := time.Now().Add(timeout)
	// The cond has no timed wait; a helper wakes it at the deadline.
	stop := time.AfterFunc(timeout, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer stop.Stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		for _, p := range c.diags[uri] {
			if p.at.After(t) {
				return p, nil
			}
		}
		if c.readErr != nil {
			return publish{}, fmt.Errorf("publish for %s: stream ended: %w", uri, c.readErr)
		}
		if !time.Now().Before(deadline) {
			return publish{}, fmt.Errorf("publish for %s: %w after %v", uri, errTimeout, timeout)
		}
		c.cond.Wait()
	}
}

// waitVersion blocks until uri gets a publish describing version v or a
// later one, or the timeout. A burst of edits publishes once, for the
// newest version, so "or later" is what an editor waits for too.
func (c *client) waitVersion(uri string, v int, timeout time.Duration) (publish, error) {
	deadline := time.Now().Add(timeout)
	stop := time.AfterFunc(timeout, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer stop.Stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		ps := c.diags[uri]
		for k := len(ps) - 1; k >= 0; k-- {
			if ps[k].version >= v {
				return ps[k], nil
			}
		}
		if c.readErr != nil {
			return publish{}, fmt.Errorf("publish for %s: stream ended: %w", uri, c.readErr)
		}
		if !time.Now().Before(deadline) {
			return publish{}, fmt.Errorf("publish of version %d for %s: %w after %v", v, uri, errTimeout, timeout)
		}
		c.cond.Wait()
	}
}

// latestPublish is the newest publish for uri; ok is false when none came.
func (c *client) latestPublish(uri string) (publish, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ps := c.diags[uri]
	if len(ps) == 0 {
		return publish{}, false
	}
	return ps[len(ps)-1], true
}

// lastPublish is when anything was last published, for quiescence waits.
func (c *client) lastPublish() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	var last time.Time
	for _, ps := range c.diags {
		if n := len(ps); n > 0 && ps[n-1].at.After(last) {
			last = ps[n-1].at
		}
	}
	return last
}

// peakRSS reads the process high-water resident set from procfs, in kB.
func (c *client) peakRSS() (int, error) {
	return readVMHWM(fmt.Sprintf("/proc/%d/status", c.cmd.Process.Pid))
}

func readVMHWM(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				break
			}
			return strconv.Atoi(f[0])
		}
	}
	return 0, fmt.Errorf("no VmHWM in %s", path)
}

// initialize runs the handshake and returns the initialize reply latency
// measured from process spawn.
func (c *client) initialize(rootURI string, initOpts any) (time.Duration, error) {
	var root any
	if rootURI != "" {
		root = rootURI
	}
	r, _, err := c.call("initialize", map[string]any{
		"processId":             os.Getpid(),
		"rootUri":               root,
		"capabilities":          map[string]any{},
		"initializationOptions": initOpts,
	})
	if err != nil {
		return 0, err
	}
	if r.err != nil {
		return 0, fmt.Errorf("initialize: %s", r.err.Message)
	}
	var res struct {
		Capabilities struct {
			SemanticTokensProvider struct {
				Full json.RawMessage `json:"full"`
			} `json:"semanticTokensProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(r.result, &res); err != nil {
		return 0, fmt.Errorf("initialize result: %w", err)
	}
	var full struct {
		Delta bool `json:"delta"`
	}
	// "full" is true or {"delta":bool}; a bool fails to decode as the
	// object, which correctly leaves delta off.
	c.delta = json.Unmarshal(res.Capabilities.SemanticTokensProvider.Full, &full) == nil && full.Delta
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return 0, err
	}
	return r.at.Sub(c.spawned), nil
}

// close ends the session cleanly, killing the process if it will not go.
func (c *client) close() error {
	var errs []error
	if _, _, err := c.call("shutdown", nil); err != nil {
		errs = append(errs, err)
	}
	if err := c.notify("exit", nil); err != nil {
		errs = append(errs, err)
	}
	_ = c.stdin.Close()
	waited := make(chan error, 1)
	go func() { waited <- c.cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			errs = append(errs, fmt.Errorf("server exit: %w", err))
		}
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		<-waited
		errs = append(errs, errors.New("server did not exit; killed"))
	}
	return errors.Join(errs...)
}
