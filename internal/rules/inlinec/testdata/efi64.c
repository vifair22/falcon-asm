/* Excerpts from airies-boot's falcon-efi and falcon-hook (GPL-3.0-or-later,
 * same author), pinned as a regression fixture for the inline asm rules:
 * every statement here is correct as written and must produce no finding
 * in 64-bit mode. */

UINT64 tsc_read(void)
{
    UINT32 lo, hi;

    __asm__ volatile("rdtsc" : "=a"(lo), "=d"(hi));
    return ((UINT64)hi << 32) | lo;
}

static void cpuid(UINT32 leaf, UINT32 r[4])
{
    __asm__ volatile("cpuid"
                     : "=a"(r[0]), "=b"(r[1]), "=c"(r[2]), "=d"(r[3])
                     : "a"(leaf), "c"(0u));
}

static int have_rdrand(void)
{
    uint32_t a = 1, b, c = 0, d;

    __asm__ volatile("cpuid" : "+a"(a), "=b"(b), "+c"(c), "=d"(d));
    return (int)((c >> 30) & 1u);
}

/* One RDRAND word; a failed one (carry clear) is retried a few times, as
 * Intel's guidance has it, and then given up on. */
static int rdrand64(uint64_t *v)
{
    int i;

    for (i = 0; i < 10; i++) {
        uint8_t ok;

        __asm__ volatile("rdrand %0; setc %1" : "=r"(*v), "=qm"(ok) : : "cc");
        if (ok)
            return 1;
    }
    return 0;
}

/* falcon-hook: startup and system calls, x86 Linux, in either width. See
 * sys.h.
 *
 * The hook is built twice, x86-64 and i686, because it runs inside the
 * booted kernel's initramfs and has to be the kernel's width: falcon-bios
 * boots 32-bit kernels as well as 64-bit ones (DESIGN Section 6.4).
 * Everything above this file is the same source for both. What
 * differs is how a system call is made and how the process starts, and each
 * of those is one short block below; <asm/unistd.h> gives the right numbers
 * for whichever width the compiler is targeting.
 */

#include "sys.h"

#include <asm/unistd.h>

#define AT_FDCWD (-100)
#define SIGCHLD  17

#if defined(__x86_64__)

static long sc6(long n, long a, long b, long c, long d, long e, long f)
{
    long ret;
    register long r10 __asm__("r10") = d;
    register long r8 __asm__("r8") = e;
    register long r9 __asm__("r9") = f;

    __asm__ volatile("syscall"
                     : "=a"(ret)
                     : "a"(n), "D"(a), "S"(b), "d"(c), "r"(r10), "r"(r8),
                       "r"(r9)
                     : "rcx", "r11", "memory");
    return ret;
}

/* Opening a file larger than 2 GiB is ordinary on x86-64. */
#define O_LARGE 0

#elif defined(__i386__)

/* int $0x80 with the arguments in ebx, ecx, edx, esi, edi. A sixth would go
 * in ebp, which the compiler will not hand over as an operand; nothing here
 * makes a call with six, so f is required to be zero rather than carried. */
static long sc6(long n, long a, long b, long c, long d, long e, long f)
{
    long ret;

    (void)f;
    __asm__ volatile("int $0x80"
                     : "=a"(ret)
                     : "a"(n), "b"(a), "c"(b), "d"(c), "S"(d), "D"(e)
                     : "memory");
    return ret;
}

/* O_LARGEFILE, which a 32-bit process has to ask for or an ISO past 2 GiB
 * fails to open with EOVERFLOW. */
#define O_LARGE 0100000

#endif
