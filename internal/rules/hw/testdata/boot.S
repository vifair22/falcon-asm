/* Excerpt of airies-boot falcon-bios/src/start/boot.S: the MBR's disk
 * address packet, the read call and the signature. */
	.code16
	.section .text.stage1, "ax"
_start:
	cli
	movw    $dap, %si
	movb    $0x42, %ah
	movb    $0x80, %dl
	int     $0x13
	ljmp    $0, $0x7E00

dap:
	.byte   16                   /* its size */
	.byte   0
	.word   0                    /* sectors, this call */
	.word   0                    /* offset */
	.word   0                    /* segment, always 0 */
	.quad   0                    /* LBA */
	.long   0                    /* sectors per track */
	.long   0                    /* heads */

	.org    _start + 440
	. = _start + 510
	.byte   0x55, 0xAA
