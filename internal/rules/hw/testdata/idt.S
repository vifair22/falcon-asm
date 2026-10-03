/* Excerpt of airies-boot falcon-bios/src/start/idt.S: an IDT built at run
 * time into a zeroed table, plus a static gate for the hover. */
#define SEL_CODE32 0x08

	.code32
fault_install:
	movl    $idt, %edi
	movw    $SEL_CODE32, 2(%edi)
	movw    $0x8E00, 4(%edi)     /* present, DPL 0, 32-bit interrupt gate */
	lidt    idt_desc
	ret

	.balign 4
idt_desc:
	.word   32 * 8 - 1
	.long   idt

	.balign 8
idt:
	.quad   0x00008E0000081000   /* vector 0: 32-bit interrupt gate */
	.space  31 * 8
