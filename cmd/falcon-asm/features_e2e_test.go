package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLensSymbolsCallsE2E drives the real server and engine through the
// size budget lens (with real gcc), workspace symbols and the call
// hierarchy, with the capabilities the client sees at initialize.
func TestLensSymbolsCallsE2E(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not on PATH")
	}
	root := t.TempDir()
	boot := "\t.code16\n" + // 0
		"\t.section .text.stage1, \"ax\"\n" + // 1
		"\t.globl _start\n" + // 2
		"_start:\n" + // 3
		"\tcall puts\n" + // 4
		"\tjmp stage2_entry\n" + // 5
		"puts:\n" + // 6
		"\tret\n" + // 7
		"\t. = _start + 510\n" + // 8
		"\t.byte 0x55, 0xAA\n" // 9
	stage2 := "\t.code16\n\t.globl stage2_entry\nstage2_entry:\n\tcall puts2\n\thlt\nputs2:\n\tret\n"
	for name, text := range map[string]string{"boot.S": boot, "stage2.S": stage2} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db := fmt.Sprintf(`[{"directory":%q,"file":"boot.S","command":"gcc -c boot.S -o boot.o"}]`, root)
	if err := os.WriteFile(filepath.Join(root, "compile_commands.json"), []byte(db), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + filepath.Join(root, "boot.S")

	c := startRun(t)
	raw := c.call("initialize", map[string]any{"processId": nil, "rootUri": "file://" + root, "capabilities": map[string]any{}})
	var init struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &init); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"codeLensProvider": `{"resolveProvider":false}`, "workspaceSymbolProvider": "true", "callHierarchyProvider": "true"} {
		if got := string(init.Capabilities[k]); got != want {
			t.Fatalf("capability %s = %s, want %s", k, got, want)
		}
	}
	c.send("initialized", nil, map[string]any{})
	c.send("textDocument/didOpen", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "falcon-asm", "version": 1, "text": boot}})
	c.waitDiags(uri, func(p published) bool { return p.Version != nil && *p.Version == 1 })

	doc := map[string]string{"uri": uri}
	var lens string
	for i := 0; i < 200 && lens == ""; i++ {
		var ls []struct {
			Range   struct{ Start struct{ Line int } } `json:"range"`
			Command struct{ Title, Command string }    `json:"command"`
		}
		if err := json.Unmarshal(c.call("textDocument/codeLens", map[string]any{"textDocument": doc}), &ls); err != nil {
			t.Fatal(err)
		}
		if len(ls) == 1 {
			lens = fmt.Sprintf("%d:%s:%s", ls[0].Range.Start.Line, ls[0].Command.Title, ls[0].Command.Command)
		}
		if lens == "" {
			// The toolchain run lands asynchronously after open.
			c.waitDiags(uri, func(published) bool { return true })
		}
	}
	if lens != "3:stage1: 7 / 510 bytes (503 free):" {
		t.Fatalf("lens = %q", lens)
	}

	var syms []struct {
		Name          string `json:"name"`
		ContainerName string `json:"containerName"`
	}
	if err := json.Unmarshal(c.call("workspace/symbol", map[string]any{"query": "puts"}), &syms); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range syms {
		names = append(names, s.Name+"@"+s.ContainerName)
	}
	if strings.Join(names, " ") != "puts@boot.S puts2@stage2.S" {
		t.Fatalf("workspace symbols = %v", names)
	}

	items := c.call("textDocument/prepareCallHierarchy", map[string]any{"textDocument": doc, "position": map[string]int{"line": 3, "character": 1}})
	var prep []map[string]any
	if err := json.Unmarshal(items, &prep); err != nil || len(prep) != 1 || prep[0]["name"] != "_start" {
		t.Fatalf("prepare = %s (%v)", items, err)
	}
	var out []struct {
		To struct {
			Name   string `json:"name"`
			Detail string `json:"detail"`
		} `json:"to"`
	}
	if err := json.Unmarshal(c.call("callHierarchy/outgoingCalls", map[string]any{"item": prep[0]}), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].To.Name != "puts" || out[1].To.Name != "stage2_entry" || !strings.Contains(out[1].To.Detail, "tail call") {
		t.Fatalf("outgoing = %+v", out)
	}
	stage2Item := c.call("textDocument/prepareCallHierarchy", map[string]any{"textDocument": doc, "position": map[string]int{"line": 5, "character": 6}})
	var s2 []map[string]any
	if err := json.Unmarshal(stage2Item, &s2); err != nil || len(s2) != 1 {
		t.Fatalf("prepare stage2 = %s", stage2Item)
	}
	var in []struct {
		From struct{ Name string } `json:"from"`
	}
	if err := json.Unmarshal(c.call("callHierarchy/incomingCalls", map[string]any{"item": s2[0]}), &in); err != nil || len(in) != 1 || in[0].From.Name != "_start" {
		t.Fatalf("incoming = %+v (%v)", in, err)
	}

	c.call("shutdown", nil)
	c.send("exit", nil, nil)
	if code := <-c.code; code != 0 {
		t.Fatalf("exit code = %d", code)
	}
}
