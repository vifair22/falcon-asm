/* Excerpt of airies-boot falcon-bios/src/start/pmode.S: the real-mode to
 * protected-mode switch and its GDT. SEL_* come from biosdefs.h. */
#define SEL_CODE32 0x08
#define SEL_DATA32 0x10

	.code16
	.section .text.stage2, "ax"
pmode_enter:
	cli
	movl    $1, %eax
	cpuid
	andl    $((1 << 24) | (1 << 25)), %edx
	cmpl    $((1 << 24) | (1 << 25)), %edx
	jne     2f
	movl    %cr4, %eax
	orl     $((1 << 9) | (1 << 10)), %eax    /* OSFXSR, OSXMMEXCPT */
	movl    %eax, %cr4
2:
	lgdt    gdt_desc
	movl    %cr0, %eax
	orl     $1, %eax             /* CR0.PE */
	movl    %eax, %cr0
	ljmpl   $SEL_CODE32, $pmode_32

	.code32
pmode_32:
	movw    $SEL_DATA32, %ax
	movw    %ax, %ds
	movw    %ax, %es
	movw    %ax, %ss
	movw    %ax, %fs
	movw    %ax, %gs
	movl    $0x7C00, %esp
	jmp     *pmode_entry

	.code16
	.balign 8
gdt:
	.quad   0                    /* the mandatory null descriptor */
	.quad   0x00CF9A000000FFFF   /* code32: present, ring 0, exec/read */
	.quad   0x00CF92000000FFFF   /* data32: present, ring 0, read/write */
	.quad   0x008F9A000000FFFF   /* code16 */
	.quad   0x008F92000000FFFF   /* data16 */
gdt_end:

	.balign 4
gdt_desc:
	.word   gdt_end - gdt - 1
	.long   gdt

	.balign 4
pmode_entry:
	.long   0
