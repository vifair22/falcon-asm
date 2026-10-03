/* falcon-boot: the AVX register state, borrowed and given back. See
 * xstate.h. Built with the baseline flags: nothing here may be an
 * instruction the processor lacks before CPUID has said it has it. */

#include <crypto/xstate.h>

#define XCR0_RESET    XCR0_X87

static uintptr_t cr4_read(void)
{
    uintptr_t v;

    __asm__ volatile ("mov %%cr4, %0" : "=r"(v));
    return v;
}

static void cr4_write(uintptr_t v)
{
    __asm__ volatile ("mov %0, %%cr4" : : "r"(v) : "memory");
}

static uint64_t xcr0_read(void)
{
    uint32_t lo, hi;

    __asm__ volatile ("xgetbv" : "=a"(lo), "=d"(hi) : "c"(0u));
    return ((uint64_t)hi << 32) | lo;
}

static void xcr0_write(uint64_t v)
{
    __asm__ volatile ("xsetbv" : : "a"((uint32_t)v),
                      "d"((uint32_t)(v >> 32)), "c"(0u) : "memory");
}

void xstate_return(xstate_saved *s)
{
    /* Excerpt: the tail of xstate_return. The clobbers matter on x86-64, where the MS ABI has the caller keep
     * xmm6..15 across a call; falcon-bios is built with no SSE registers
     * for the compiler to keep anything in. */
#if defined(__x86_64__)
    __asm__ volatile ("vzeroall" : : : "memory",
                      "xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5",
                      "xmm6", "xmm7", "xmm8", "xmm9", "xmm10", "xmm11",
                      "xmm12", "xmm13", "xmm14", "xmm15");
#else
    __asm__ volatile ("vzeroall" : : : "memory");
#endif
    xcr0_write(s->xcr0);
    if ((s->cr4 & CR4_OSXSAVE) == 0u) {
        cr4_write(s->cr4);
    }
}
