package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
