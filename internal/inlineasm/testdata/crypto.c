/* falcon-boot: shared crypto plumbing. See crypto.h. */

#include <crypto/crypto.h>


#if defined(__x86_64__) || defined(_M_X64) || defined(__i386__)
/* XCR0, through XGETBV. Only reached after CPUID has reported OSXSAVE,
 * since the instruction is #UD while CR4.OSXSAVE is clear. */
static uint64_t xcr0_read(void)
{
    uint32_t lo, hi;

    __asm__ volatile ("xgetbv" : "=a"(lo), "=d"(hi) : "c"(0u));
    return ((uint64_t)hi << 32) | lo;
}
#endif

