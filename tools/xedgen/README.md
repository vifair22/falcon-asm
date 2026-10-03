# xedgen

Generates `internal/isa/data/xed.json`, the bulk x86 / x86-64 instruction
table behind falcon-asm's hover and semantic tokens. It merges two upstream
datasets:

- **Intel XED** datafiles: the authoritative structure. Every instruction
  class (ICLASS), its CATEGORY, EXTENSION, ISA_SET, CPL, flag effects,
  attributes, operand forms and 16/32/64-bit mode restrictions.
- **asm-lsp** x86 documentation data: the human one-line summary for each
  mnemonic, and the GAS spellings of its forms (used for aliases such as
  `cwtl` for `cwde`).

The join key is the canonical Intel mnemonic. The output is the JSON
encoding of `[]isa.Instruction` (the generator marshals `internal/isa`'s
own types, so the schema cannot drift), one instruction per line, sorted by
name, pure ASCII.

The hand-written `internal/isa/data/system.json` takes precedence over this
file for any name present in both; see `docs/DESIGN.md`.

## Pinned sources

| Source  | Version     | Commit                                     | License      |
|---------|-------------|--------------------------------------------|--------------|
| XED     | `v2026.08.23` (annotated tag object `c6424829fd66ef8c3abb1ecb27a8d4003abebf6f`) | `0bcb6237345c5066726dcc08b3d87928df3b5b26` | Apache-2.0 |
| asm-lsp | `v0.10.1`   | `bacbe5eedfbfc202afe8f517faef2d5b9152bf95` | BSD-2-Clause |

Files read:

- XED: every `*.cfg` under `datafiles/`; from them, each file listed as
  `dec-instructions` (the `*-isa.txt` / `*-isa.xed.txt` instruction files,
  AVX, AVX-512, AMX, APX, VMX, SGX, AMD and VIA included) and each `widths`
  table. Encoder-only files (`xed-nops.txt`) are not read.
- asm-lsp: `docs_store/opcodes/x86_64.xml` and `docs_store/opcodes/x86.xml`
  (the PeachPy "Opcodes" XML schema; summaries and `gas-name` attributes),
  then `docs_store/instr_info_cache/x86_instr_docs.html` (the instruction
  index derived from Felix Cloutier's x86 reference, itself derived from the
  Intel SDM), which supplies summaries for the system and privileged
  instructions the opcodes XML does not cover. Only the one-line titles are
  taken from it.

## Regenerating

```sh
mkdir -p build
git clone --depth 1 --branch v2026.08.23 https://github.com/intelxed/xed.git build/xed
git clone --depth 1 --branch v0.10.1 https://github.com/bergercookie/asm-lsp.git build/asm-lsp
go run ./tools/xedgen -xed build/xed -asmlsp build/asm-lsp \
    -out internal/isa/data/xed.json -fallbacks build/xedgen-fallbacks.txt
ascii-check internal/isa/data/xed.json
go test ./internal/isa ./tools/xedgen
```

`build/` is gitignored. The run prints record, instruction, form and byte
counts plus the number of XED-derived summaries. `-fallbacks` writes the
names of those instructions to a file for auditing.

When bumping a pin, update the table above and the attribution in the
repository NOTICE, regenerate, and review the diff of `xed.json`: it is one
instruction per line, so changes are readable.

## What the generator does

1. **Load XED.** Walk every `*.cfg`, read each instruction file once, and
   flatten every `{ ... }` record so each PATTERN/OPERANDS pair becomes one
   record carrying the block's header keys. Backslash-continued lines are
   joined. `UDELETE` (by UNAME) and `DELETE` (by ICLASS) are collected
   across all files and applied afterwards, as XED's own generator does.
   A record without `ISA_SET` takes its `EXTENSION`, as in XED.
2. **Name.** Each record is filed under a canonical mnemonic:
   - dropped: `*_LOCK` and `REP_` / `REPE_` / `REPNE_` duplicates (the
     prefix is lexed separately), `PREFETCH_RESERVED`, and records with the
     `UNDOCUMENTED` attribute (alias encodings such as `SHL /6`, the 8087
     `FENI`/`FDISI` NOPs), except `FFREEP`, which GAS assembles;
   - merged: `CALL_NEAR`/`CALL_FAR` into `call`, likewise `jmp` and `ret`,
     `MOV_CR`/`MOV_DR` into `mov`, `MOVSD_XMM` into `movsd`, `SYSRET64` and
     `SYSRET_AMD` into `sysret`, `PCMPESTRI64` into `pcmpestri`, ...;
   - VIA PadLock `REP_*` iclasses keep their name without the prefix;
   - the dword string `MOVSD` / `CMPSD` become `movsl` / `cmpsl`, the only
     spelling that does not collide with the SSE2 instructions.
3. **Forms.** Operands are rendered in the SDM's notation (`r/m32`, `imm8`,
   `xmm/m128`, `m32bcst`, `vm32y`, `m16&32`, `m80fp`, `ptr16:32`, ...) and
   then put in AT&T order (XED's `ATT_OPERAND_ORDER_EXCEPTION` keeps
   `enter`, `extrq` and `insertq` in Intel order). Operand-size dependent
   records are rendered at 16, 32 and 64 bits, pruned by `not64`,
   `mode16/32`, REX.W and VEX.W constraints. AVX-512 write masks decorate
   the destination (`zmm{k}{z}`). Register and memory variants are fused
   into `r/mN` / `xmm/mN`, first per encoding, then across encodings, then
   the broadcast variant is folded in. Forms another form already covers
   are dropped.
   - AVX-512 embedded-rounding and SAE variants are not listed: they are
     the register form plus a `{rn-sae}` style decoration.
   - APX (EVEX map 4) promotions of legacy instructions are not listed when
     the instruction has legacy forms; APX-only instructions keep theirs.
   - `nop` lists only 0x90 and 0F 1F /0, not the reserved-NOP hint space.
   - x87 memory forms omit the implicit `st(0)`.
4. **Attributes.** CPL is 3 if any merged record is CPL 3, otherwise 0
   (so merging `MOV_CR` does not make `mov` privileged). Modes is the union
   of the records' mode restrictions: `"64"`, `"16,32"` or `""` for all.
   Flags are the union across records: `tst` is read, `u` is undefined,
   everything else (`mod`, `0`, `1`, `pop`, `ah`) is written; a flag both
   written and undefined in different cases (OF after a multi-bit shift)
   is reported as written. x87 condition codes appear as `C0`..`C3`.
   Category comes from the record whose ICLASS matches the name.
   Each form's level comes from `isa.LevelForISASet`.
5. **Summaries.** asm-lsp is searched under the name, then under related
   keys (`LOOPcc`, `Jcc`, `FCMOVcc`, `UD`, the string stem for `SCASQ`, the
   name without a `64` suffix). A short table in `build.go`
   (`manualSummary`) covers well-known instructions neither asm-lsp file
   describes (AMD SVM, FRED, MSR lists, `salc`, `ffreep`). Anything else
   gets `"<NAME> instruction (<ISA_SET>)"`; that fixed shape makes them easy
   to grep for and replace. Upstream text is transliterated to ASCII
   (smart quotes, dashes, ellipses, trademark signs).
6. **Aliases.** GAS spellings that Lookup's suffix stripping cannot reach
   on its own: asm-lsp `gas-name` values that are not name+`[bwlq]`
   (`cltq`, `cwtl`, `vcvtpd2dqx`), alternative condition-code spellings
   (`jae` for `jnb`, `cmove` for `cmovz`, `setc` for `setb`), and a short
   manual list (`ljmp`, `lcall`, `lret`, `sal`, `movabs`, `lodsl`, `wait`,
   `icebp`, `loopz`). `movzx` / `movsx` / `movsxd` spellings (`movzbl`,
   `movslq`, ...) are left to `system.json`. An alias that is another
   instruction's name plus a size suffix (`pushfl`, `iretl`) is not
   emitted, because Lookup already splits it.

## Testing

```sh
go test -race -cover ./tools/xedgen
```

The tests use small inline fixtures (XED records, a miniature `datafiles`
tree, asm-lsp XML and HTML snippets) and never touch the clones in
`build/`.

## Licenses and attribution

The generated `xed.json` is a derived work of both sources.

- **Intel XED** is licensed under the Apache License, Version 2.0.
  Copyright (c) Intel Corporation. <https://github.com/intelxed/xed>
- **asm-lsp** is licensed under the BSD 2-Clause License.
  Copyright (c) 2024 Nikos Koukis; Copyright (c) 2017 Facebook Inc.;
  Copyright (c) 2014-2017, Georgia Institute of Technology. All rights
  reserved. <https://github.com/bergercookie/asm-lsp>
  Its x86 instruction index (`x86_instr_docs.html`) is derived from
  Felix Cloutier's x86 reference (<https://www.felixcloutier.com/x86/>),
  which is in turn derived from the Intel 64 and IA-32 Architectures
  Software Developer's Manual.

The BSD-2-Clause notice and disclaimer, and the Apache-2.0 attribution,
are reproduced in the repository's NOTICE file.
