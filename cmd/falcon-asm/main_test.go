package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
)

func frame(body string) string {
	return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

// session is the minimal clean lifecycle: it must end with exit code 0.
var session = frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":null,"rootUri":null,"capabilities":{}}}`) +
	frame(`{"jsonrpc":"2.0","method":"initialized","params":{}}`) +
	frame(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`) +
	frame(`{"jsonrpc":"2.0","method":"exit"}`)

// checkSession verifies the two responses a clean session produces.
func checkSession(t *testing.T, stdout []byte) {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(stdout))
	initResp, err := jsonrpc.Read(r)
	if err != nil {
		t.Fatalf("read initialize response: %v (stdout %q)", err, stdout)
	}
	var res struct {
		Capabilities struct {
			SemanticTokensProvider struct {
				Legend struct {
					TokenTypes []string `json:"tokenTypes"`
				} `json:"legend"`
			} `json:"semanticTokensProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(initResp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if got := res.Capabilities.SemanticTokensProvider.Legend.TokenTypes; len(got) == 0 || got[0] != "keyword" {
		t.Fatalf("legend not wired: %v", got)
	}
	shut, err := jsonrpc.Read(r)
	if err != nil {
		t.Fatalf("read shutdown response: %v", err)
	}
	if string(shut.Result) != "null" || shut.Error != nil {
		t.Fatalf("shutdown response = %+v", shut)
	}
}

// TestBinary builds the real binary, with version stamping, and drives it
// over stdio the way an editor does.
func TestBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "falcon-asm")
	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X main.version=9.8.7 -X main.buildTime=20260101.0000", ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got := string(out); !strings.Contains(got, "9.8.7") || !strings.Contains(got, "20260101.0000") {
		t.Fatalf("--version printed %q", got)
	}

	cmd := exec.Command(bin, "--stdio")
	cmd.Stdin = strings.NewReader(session)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("server run: %v\nstderr: %s", err, stderr.String())
	}
	checkSession(t, stdout.Bytes())

	// Stream closed without shutdown/exit: the LSP-mandated exit code is 1.
	cmd = exec.Command(bin)
	cmd.Stdin = strings.NewReader("")
	err = cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("abrupt end: err = %v, want exit status 1", err)
	}
}

// TestRun covers the flag handling in-process, where coverage is measured.
func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantCode   int
		wantStdout string
		wantStderr string
		session    bool
	}{
		{name: "version", args: []string{"--version"}, wantStdout: "falcon-asm dev (built dev)\n"},
		{name: "single dash version", args: []string{"-version"}, wantStdout: "falcon-asm dev"},
		{name: "help", args: []string{"--help"}, wantStderr: "-stdio"},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: 2, wantStderr: "bogus"},
		{name: "stdio session", args: []string{"--stdio", "--clientProcessId=42"}, stdin: session, session: true},
		{name: "no exit", args: nil, stdin: "", wantCode: 1, wantStderr: "without exit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if tt.wantStdout != "" && !strings.HasPrefix(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want prefix %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
			if tt.session {
				checkSession(t, stdout.Bytes())
			}
		})
	}
}

// lspClient drives run() in-process over pipes, splitting responses from
// the notifications the engine publishes asynchronously.
type lspClient struct {
	t      *testing.T
	in     *io.PipeWriter
	resp   chan *jsonrpc.Message
	notes  chan *jsonrpc.Message
	nextID int
	code   chan int
}

func startRun(t *testing.T) *lspClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &lspClient{t: t, in: inW, resp: make(chan *jsonrpc.Message, 8), notes: make(chan *jsonrpc.Message, 64), code: make(chan int, 1)}
	go func() {
		code := run([]string{"--stdio"}, inR, outW, io.Discard)
		_ = outW.Close()
		c.code <- code
	}()
	go func() {
		r := bufio.NewReader(outR)
		for {
			m, err := jsonrpc.Read(r)
			if err != nil {
				close(c.resp)
				return
			}
			if m.IsNotification() {
				c.notes <- m
			} else {
				c.resp <- m
			}
		}
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

func (c *lspClient) send(method string, id *json.RawMessage, params any) {
	c.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := jsonrpc.Write(c.in, &jsonrpc.Message{JSONRPC: "2.0", ID: id, Method: method, Params: raw}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *lspClient) call(method string, params any) json.RawMessage {
	c.t.Helper()
	c.nextID++
	id := json.RawMessage(strconv.Itoa(c.nextID))
	c.send(method, &id, params)
	select {
	case m := <-c.resp:
		if m == nil || m.Error != nil {
			c.t.Fatalf("%s: %+v", method, m)
		}
		return m.Result
	case <-time.After(20 * time.Second):
		c.t.Fatalf("%s: no response", method)
	}
	return nil
}

type published struct {
	URI         string `json:"uri"`
	Version     *int   `json:"version"`
	Diagnostics []struct {
		Range struct {
			Start struct{ Line int } `json:"start"`
		} `json:"range"`
		Source  string `json:"source"`
		Message string `json:"message"`
	} `json:"diagnostics"`
}

// waitDiags returns the first publish for uri that ok accepts.
func (c *lspClient) waitDiags(uri string, ok func(published) bool) published {
	c.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case m := <-c.notes:
			var p published
			if m.Method != "textDocument/publishDiagnostics" || json.Unmarshal(m.Params, &p) != nil || p.URI != uri {
				continue
			}
			if ok(p) {
				return p
			}
		case <-deadline:
			c.t.Fatalf("no matching diagnostics for %s", uri)
		}
	}
}

func gasMessages(p published) []string {
	var out []string
	for _, d := range p.Diagnostics {
		if d.Source == "gas" {
			out = append(out, fmt.Sprintf("%d:%s", d.Range.Start.Line, d.Message))
		}
	}
	return out
}

// TestToolchainE2E runs the real gcc through the whole server: a compile
// database in a temp workspace, assembler errors published asynchronously
// after a save, and listing hints with offsets and the boot-sector padding.
func TestToolchainE2E(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not on PATH")
	}
	root := t.TempDir()
	file := filepath.Join(root, "boot.S")
	clean := "\t.code16\n" + // 0
		"\t.section .text, \"ax\"\n" + // 1
		"\t.globl _start\n" + // 2
		"_start:\n" + // 3
		"\tmovw $0x2401, %ax\n" + // 4
		"\n" + // 5
		"\t. = _start + 510\n" + // 6
		"\t.byte 0x55, 0xAA\n" // 7
	broken := strings.Replace(clean, "\n\n", "\n\tbogus %ax\n", 1)
	write := func(s string) {
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(broken)
	db := fmt.Sprintf(`[{"directory":%q,"file":"boot.S","command":"gcc -m32 -c boot.S -o boot.o"}]`, root)
	if err := os.WriteFile(filepath.Join(root, "compile_commands.json"), []byte(db), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + file

	c := startRun(t)
	c.call("initialize", map[string]any{"processId": nil, "rootUri": "file://" + root, "capabilities": map[string]any{},
		"initializationOptions": map[string]any{"toolchain": true, "listingHints": true, "level": "", "compileCommands": ""}})
	c.send("initialized", nil, map[string]any{})

	hints := func() string {
		raw := c.call("textDocument/inlayHint", map[string]any{"textDocument": map[string]string{"uri": uri},
			"range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 99, "character": 0}}})
		var hs []struct {
			Position struct{ Line int } `json:"position"`
			Label    string             `json:"label"`
		}
		if err := json.Unmarshal(raw, &hs); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hs {
			out = append(out, fmt.Sprintf("%d:%s", h.Position.Line, h.Label))
		}
		return strings.Join(out, "|")
	}
	edit := func(v int, text string, save bool) {
		write(text)
		c.send("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": v},
			"contentChanges": []map[string]string{{"text": text}}})
		if save {
			c.send("textDocument/didSave", nil, map[string]any{"textDocument": map[string]string{"uri": uri}})
		}
	}
	isBogus := func(v int) func(published) bool {
		return func(p published) bool {
			m := gasMessages(p)
			return p.Version != nil && *p.Version == v && len(m) == 1 && strings.HasPrefix(m[0], "5:") && strings.Contains(m[0], "bogus")
		}
	}

	// Open runs the toolchain: gas's error arrives asynchronously, and with
	// errors gas cannot finalize addresses, so there are no hints.
	c.send("textDocument/didOpen", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "falcon-asm", "version": 1, "text": broken}})
	c.waitDiags(uri, isBogus(1))
	if h := hints(); h != "" {
		t.Fatalf("hints with assembly errors = %q", h)
	}

	// Fix and save. Until the save's run lands, every publish still carries
	// the old gas error, so a clean publish for version 2 is the run's.
	edit(2, clean, true)
	c.waitDiags(uri, func(p published) bool { return p.Version != nil && *p.Version == 2 && len(gasMessages(p)) == 0 })
	if got, want := hints(), "4:+0x0  b8 01 24|6:pads 507 bytes|7:+0x1fe  55 aa"; got != want {
		t.Fatalf("hints = %q, want %q", got, want)
	}
	// An unsaved edit hides the hints rather than misplacing them.
	edit(3, "\n"+clean, false)
	if h := hints(); h != "" {
		t.Fatalf("hints while dirty = %q", h)
	}

	// Break it again and save: the error is published after the save.
	edit(4, broken, true)
	c.waitDiags(uri, isBogus(4))

	c.call("shutdown", nil)
	c.send("exit", nil, nil)
	if code := <-c.code; code != 0 {
		t.Fatalf("exit code = %d", code)
	}
}
