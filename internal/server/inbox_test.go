package server

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// gateEngine blocks every hover until the test releases it, so messages
// pile up in the inbox behind it.
type gateEngine struct {
	NopEngine
	entered chan struct{}
	release chan struct{}
}

func (g *gateEngine) Hover(*docstore.Document, protocol.Position) (*protocol.Hover, error) {
	g.entered <- struct{}{}
	<-g.release
	return nil, nil
}

func (g *gateEngine) SemanticTokens(*docstore.Document) []uint32 { return []uint32{0, 1, 3, 0, 0} }

// queuedLen is how many messages wait in the inbox.
func (in *inbox) queuedLen() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.q) - in.head
}

// TestCancelAndStale queues requests behind a blocked one and checks each
// is answered the way its situation demands: cancelled, stale, or computed.
func TestCancelAndStale(t *testing.T) {
	eng := &gateEngine{entered: make(chan struct{}), release: make(chan struct{})}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := New(Options{Engine: eng})
	done := make(chan int, 1)
	go func() { done <- srv.Run(inR, outW); _ = outW.Close() }()
	resp := make(chan *jsonrpc.Message, 32)
	go func() {
		r := jsonrpc.NewConn(outR, io.Discard)
		for {
			m, err := r.Read()
			if err != nil {
				close(resp)
				return
			}
			if !m.IsNotification() {
				resp <- m
			}
		}
	}()
	out := jsonrpc.NewConn(nil, inW)
	send := func(id int, method string, params any) {
		t.Helper()
		raw, _ := json.Marshal(params)
		m := &jsonrpc.Message{Method: method, Params: raw}
		if id > 0 {
			rid := json.RawMessage(mustJSON(t, id))
			m.ID = &rid
		}
		if err := out.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	next := func() *jsonrpc.Message {
		t.Helper()
		select {
		case m := <-resp:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no response")
			return nil
		}
	}
	doc := map[string]any{"textDocument": map[string]any{"uri": testURI}}
	change := func(v int) map[string]any {
		return map[string]any{
			"textDocument":   map[string]any{"uri": testURI, "version": v},
			"contentChanges": []map[string]any{{"text": "\tnop\n"}},
		}
	}

	send(1, protocol.MethodInitialize, map[string]any{"capabilities": map[string]any{}})
	next()
	send(0, protocol.MethodInitialized, map[string]any{})
	send(0, protocol.MethodDidOpen, map[string]any{"textDocument": map[string]any{
		"uri": testURI, "languageId": "falcon-asm", "version": 1, "text": "\tcli\n"}})

	send(2, protocol.MethodHover, map[string]any{"textDocument": doc["textDocument"], "position": protocol.Position{}})
	<-eng.entered
	send(3, protocol.MethodSemanticTokensFull, doc)  // cancelled below
	send(4, protocol.MethodSemanticTokensFull, doc)  // stale: a change follows
	send(7, protocol.MethodSemanticTokensDelta, doc) // stale too
	send(0, protocol.MethodDidChange, change(2))     //
	send(5, protocol.MethodSemanticTokensFull, doc)  // current: answered
	send(0, protocol.MethodCancelRequest, map[string]int{"id": 3})
	send(0, protocol.MethodCancelRequest, map[string]int{"id": 2})  // in progress: ignored
	send(0, protocol.MethodCancelRequest, map[string]int{"id": 99}) // unknown: ignored
	send(6, protocol.MethodShutdown, nil)
	send(0, protocol.MethodCancelRequest, map[string]int{"id": 6}) // shutdown always runs
	deadline := time.Now().Add(5 * time.Second)
	for srv.in.queuedLen() < 6 {
		if time.Now().After(deadline) {
			t.Fatalf("inbox holds %d messages", srv.in.queuedLen())
		}
		time.Sleep(time.Millisecond)
	}
	close(eng.release)

	if m := next(); string(*m.ID) != "2" || m.Error != nil {
		t.Errorf("hover: %+v", m)
	}
	if m := next(); string(*m.ID) != "3" || m.Error == nil || m.Error.Code != jsonrpc.RequestCancelled {
		t.Errorf("cancelled request: %+v", m)
	}
	if m := next(); string(*m.ID) != "4" || m.Error == nil || m.Error.Code != jsonrpc.ContentModified {
		t.Errorf("stale request: %+v", m)
	}
	if m := next(); string(*m.ID) != "7" || m.Error == nil || m.Error.Code != jsonrpc.ContentModified {
		t.Errorf("stale delta request: %+v", m)
	}
	if m := next(); string(*m.ID) != "5" || m.Error != nil || string(m.Result) != `{"resultId":"1","data":[0,1,3,0,0]}` {
		t.Errorf("current request: %+v %s", m, m.Result)
	}
	if m := next(); string(*m.ID) != "6" || m.Error != nil {
		t.Errorf("shutdown: %+v", m)
	}
	send(0, protocol.MethodExit, nil)
	if code := <-done; code != 0 {
		t.Errorf("exit code %d", code)
	}
	_ = inW.Close()
	srv.in.mu.Lock()
	defer srv.in.mu.Unlock()
	if len(srv.in.queued) != 0 || len(srv.in.cancelled) != 0 || len(srv.in.changes) != 0 {
		t.Errorf("inbox bookkeeping leaked: %v %v %v", srv.in.queued, srv.in.cancelled, srv.in.changes)
	}
}
