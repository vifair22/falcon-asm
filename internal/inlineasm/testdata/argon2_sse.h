/* falcon-boot: Argon2id's block function on 128-bit vectors, the body
 * argon2_sse2.c and argon2_ssse3.c both compile (see argon2_simd.h).
 *
 * Excerpt: the register wipe at the end of the file. */

void SIMD_NAME(argon2_wipe)(void)
{
    __asm__ volatile ("pxor %%xmm0, %%xmm0\n\tpxor %%xmm1, %%xmm1\n\t"
                      "pxor %%xmm2, %%xmm2\n\tpxor %%xmm3, %%xmm3\n\t"
                      "pxor %%xmm4, %%xmm4\n\tpxor %%xmm5, %%xmm5\n\t"
                      "pxor %%xmm6, %%xmm6\n\tpxor %%xmm7, %%xmm7"
                      ::: "xmm0", "xmm1", "xmm2", "xmm3",
                          "xmm4", "xmm5", "xmm6", "xmm7");
#if defined(__x86_64__)
    __asm__ volatile ("pxor %%xmm8, %%xmm8\n\tpxor %%xmm9, %%xmm9\n\t"
                      "pxor %%xmm10, %%xmm10\n\tpxor %%xmm11, %%xmm11\n\t"
                      "pxor %%xmm12, %%xmm12\n\tpxor %%xmm13, %%xmm13\n\t"
                      "pxor %%xmm14, %%xmm14\n\tpxor %%xmm15, %%xmm15"
                      ::: "xmm8", "xmm9", "xmm10", "xmm11",
                          "xmm12", "xmm13", "xmm14", "xmm15");
#endif
}
