/* A protected-mode to long-mode switch in the order the SDM gives: PAE,
 * CR3, EFER.LME, PG, then a far jump into a 64-bit code segment. */
	.code32
	.globl  lm_enter
lm_enter:
	cli
	lgdt    gdt_desc
	movl    %cr4, %eax
	orl     $0x20, %eax          /* CR4.PAE */
	movl    %eax, %cr4
	movl    $pml4, %eax
	movl    %eax, %cr3
	movl    $0xC0000080, %ecx
	rdmsr
	orl     $0x100, %eax         /* EFER.LME */
	wrmsr
	movl    %cr0, %eax
	orl     $0x80000000, %eax    /* CR0.PG */
	movl    %eax, %cr0
	ljmp    $0x08, $lm64

	.code64
lm64:
	movw    $0x10, %ax
	movw    %ax, %ds
	hlt

	.balign 8
gdt:
	.quad   0
	.quad   0x00AF9A000000FFFF
	.quad   0x00CF92000000FFFF
gdt_end:
gdt_desc:
	.word   gdt_end - gdt - 1
	.long   gdt
	.balign 4096
pml4:
	.quad   0
