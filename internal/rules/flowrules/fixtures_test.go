package flowrules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The falcon-bios fixtures are copies of airies-boot's assembly, a real
// boot loader that boots: every Warning a rule here raises on them is a
// false positive by construction. The Information findings that are true
// are pinned so a change that adds or loses one is noticed.
var knownInfos = []string{
	// thunk.S patches the vector into its int instruction on purpose.
	"thunk.S:93 self-modifying-code",
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(benchdata.Dir(), "*.S"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	var out []string
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

func TestFixturesClean(t *testing.T) {
	var infos []string
	for _, name := range fixtureNames(t) {
		text := benchdata.Fixture(t, name)
		for _, f := range lintText(t, text, asm.Mode16, lint.Target{Mode: 16}, "") {
			if f.Severity.AtLeast(lint.Warning) {
				t.Errorf("%s: %v", name, f)
				continue
			}
			infos = append(infos, fmt.Sprintf("%s:%d %s", name, line(f), f.Code))
		}
	}
	if strings.Join(infos, "\n") != strings.Join(knownInfos, "\n") {
		t.Errorf("information findings:\n%s\nwant:\n%s", strings.Join(infos, "\n"), strings.Join(knownInfos, "\n"))
	}
}

// TestPmodeCriticalSection pins rule 24 on airies-boot's pmode.S: it
// repeats cli after INT 15h, so it is clean; without that cli the A20
// call re-enables interrupts ahead of lgdt and the CR0 write.
func TestPmodeCriticalSection(t *testing.T) {
	text := benchdata.Fixture(t, "pmode.S")
	expect(t, text, asm.Mode16, CodeIFCritical, 0, "")
	const repeat = "\tint     $0x15\n"
	k := strings.Index(text, repeat)
	if k < 0 {
		t.Fatal("pmode.S changed: no int $0x15")
	}
	// Drop the cli that follows the int (after its comment block).
	rest := text[k+len(repeat):]
	c := strings.Index(rest, "\tcli\n")
	if c < 0 {
		t.Fatal("pmode.S changed: no cli after the int")
	}
	broken := text[:k+len(repeat)] + rest[:c] + rest[c+len("\tcli\n"):]
	got := expect(t, broken, asm.Mode16, CodeIFCritical, 1, "INT 15h AX=2401h returns with interrupts enabled")
	if !strings.Contains(got[0].Message, "lgdt gdt_desc") && !strings.Contains(got[0].Message, "%cr") {
		t.Fatalf("message %q", got[0].Message)
	}
	if fixed := apply(t, broken, got[0].Fixes[0]); len(only(lintText(t, fixed, asm.Mode16, lint.Target{}, CodeIFCritical), CodeIFCritical)) != 0 {
		t.Fatalf("fix does not fix:\n%s", fixed)
	}
}

// TestAirieBootExcerpts pins the shapes from airies-boot that first drew
// false reports, each with the variant that must still be reported.
func TestAirieBootExcerpts(t *testing.T) {
	// rthunk.S: CR0 read in full, a bit set through %al, written back.
	expect(t, "\t.code16\nf:\n\tmovl %cr0, %eax\n\torb $1, %al\n\tmovl %eax, %cr0\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// nobar.S: a PCI address built in %ebx, low byte or'ed, sent whole.
	expect(t, "\t.code16\nf:\n\tmovl %ebx, %eax\n\torb $0x10, %al\n\tmovw $0xCF8, %dx\n\toutl %eax, %dx\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// boot.S / boot_fd.S / noe820.S: reset the drive between read retries.
	retry := "\t.code16\nf:\n\tmovw $3, %bp\nnext:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tjnc 5f\n\txorb %ah, %ah\n\tint $0x13\n\tdecw %bp\n\tjnz next\n5:\tret"
	expect(t, retry, asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	expect(t, strings.Replace(retry, "\tjnc 5f\n", "", 1), asm.Mode16, CodeBIOSCFUnchecked, 1, "INT 13h AH=02h")
	// rigboot.S: a VBR copying the partition entry from the caller's DS:SI.
	expect(t, bootSector("\tcld\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x7E10, %di\n\tmovw $16, %cx\n\trep movsb"), asm.Mode16, CodeSegmentUnset, 0, "")
}

// TestAirieBootTree lints the whole airies-boot checkout when it is there
// (FALCON_AIRIES=dir), for the corpus run the rules were tuned on.
func TestAirieBootTree(t *testing.T) {
	dir := os.Getenv("FALCON_AIRIES")
	if dir == "" {
		t.Skip("FALCON_AIRIES not set")
	}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".S") || strings.Contains(p, "/build/") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, f := range lintText(t, string(raw), asm.Mode16, lint.Target{Mode: 16}, "") {
			t.Logf("%s:%d: %s %s: %s", strings.TrimPrefix(p, dir), line(f), f.Severity, f.Code, f.Message)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
