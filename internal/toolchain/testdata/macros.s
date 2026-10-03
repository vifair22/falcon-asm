.macro m
nop
.data
.byte 5
.text
ret
.endm
.text
m
.rept 3
nop
.endr
nop
