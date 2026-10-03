package server

import (
	"bytes"
	"encoding/json"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// inbox decouples reading the stream from handling it. A read goroutine
// fills it as fast as the client writes, so while one request is being
// answered the handler can already see what came after it: a
// $/cancelRequest naming a request still queued, or a newer didChange for
// the document a queued semantic tokens request is about. Handling stays
// sequential and in arrival order; the inbox only lets the handler skip
// work whose answer nobody will use.
type inbox struct {
	mu   sync.Mutex
	cond *sync.Cond
	q    []item
	head int
	err  error // why the stream ended; reported once the queue drains

	// queued holds the ids of requests not yet taken by the handler, so a
	// cancel for a request already answered (the common race) is dropped
	// instead of being remembered forever.
	queued    map[string]bool
	cancelled map[string]bool
	// changes counts the didChange notifications queued per URI.
	changes map[string]int
}

type item struct {
	m   *jsonrpc.Message
	uri string // set for didChange
}

func newInbox() *inbox {
	in := &inbox{queued: map[string]bool{}, cancelled: map[string]bool{}, changes: map[string]int{}}
	in.cond = sync.NewCond(&in.mu)
	return in
}

// idKey normalizes a request id for map lookups. Ids are JSON numbers or
// strings; the raw text is the identity, minus incidental whitespace.
func idKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// put records a message from the read loop.
func (in *inbox) put(m *jsonrpc.Message) {
	it := item{m: m}
	var cancel string
	switch {
	case m.Method == protocol.MethodDidChange:
		var p struct {
			TextDocument protocol.TextDocumentIdentifier `json:"textDocument"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			it.uri = p.TextDocument.URI
		}
	case m.Method == protocol.MethodCancelRequest:
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			cancel = idKey(p.ID)
		}
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	switch {
	case cancel != "":
		// The cancel itself needs no handling, only its effect on the
		// queued request it names.
		if in.queued[cancel] {
			in.cancelled[cancel] = true
		}
		return
	case it.uri != "":
		in.changes[it.uri]++
	case m.IsRequest():
		in.queued[idKey(*m.ID)] = true
	}
	in.q = append(in.q, it)
	in.cond.Signal()
}

// close ends the stream; err is what the read loop failed with, never nil.
func (in *inbox) close(err error) {
	in.mu.Lock()
	in.err = err
	in.cond.Signal()
	in.mu.Unlock()
}

// take returns the next message, blocking until there is one. cancelled
// reports a request the client cancelled while it was queued. When the
// stream has ended and the queue is drained it returns the read error.
func (in *inbox) take() (m *jsonrpc.Message, cancelled bool, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for in.head == len(in.q) {
		if in.err != nil {
			return nil, false, in.err
		}
		in.cond.Wait()
	}
	it := in.q[in.head]
	in.q[in.head] = item{}
	in.head++
	if in.head == len(in.q) {
		// Reuse the backing array once drained instead of letting it grow
		// with the session.
		in.q, in.head = in.q[:0], 0
	}
	if it.uri != "" {
		if in.changes[it.uri]--; in.changes[it.uri] <= 0 {
			delete(in.changes, it.uri)
		}
	}
	if it.m.IsRequest() {
		k := idKey(*it.m.ID)
		delete(in.queued, k)
		cancelled = in.cancelled[k]
		delete(in.cancelled, k)
	}
	return it.m, cancelled, nil
}

// changePending reports whether a didChange for uri is queued behind the
// message being handled, which makes any answer about the current text
// stale before it is sent.
func (in *inbox) changePending(uri string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.changes[uri] > 0
}
