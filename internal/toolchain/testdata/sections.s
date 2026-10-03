.text
nop
.data
.long 1
.bss
buf: .space 16
.section .rodata
.byte 1
.text
.org 0x10
ret
.code64
movabsq $0x1122334455667788, %rax
.pushsection .foo,"a"
.byte 9
.popsection
nop
.previous
.byte 7
.subsection 1
.byte 8
