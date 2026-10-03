/* Excerpt of airies-boot falcon-bios/src/jump/rthunk.S: a GDT placed with
 * .org at fixed offsets, its base installed at run time. */
#define JRES_OFF_GDTR 0x08
#define JRES_OFF_GDT  0x40
#define SEL_D32 0x10

	.section .text.jres, "ax"
	.code16
jres_blob_start:
	.org    JRES_OFF_GDTR
	.word   5 * 8 - 1
	.long   0                    /* the GDT's linear address, installed */
	.org    JRES_OFF_GDT
	.quad   0
	.quad   0x00CF9A000000FFFF   /* 0x08: code32, flat */
	.quad   0x00CF92000000FFFF   /* 0x10: data32, flat */
	.quad   0x00009A000000FFFF   /* 0x18: code16, base the block, installed */
	.quad   0x000092000000FFFF   /* 0x20: data16, base 0, real mode's limits */

jres_up:
	lgdtl   %cs:JRES_OFF_GDTR
	movl    %cr0, %eax
	orb     $1, %al
	movl    %eax, %cr0
	ljmpl   *%cs:0x30

	.code32
jres_pm32:
	movw    $SEL_D32, %ax
	movw    %ax, %ds
