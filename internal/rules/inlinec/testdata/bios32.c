/* Excerpts from airies-boot's falcon-bios (GPL-3.0-or-later, same author),
 * pinned as a regression fixture for the inline asm rules: every statement
 * here is correct as written and must produce no finding in 32-bit mode. */

static inline uint8_t inb(uint16_t port)
{
    uint8_t v;

    __asm__ __volatile__("inb %1, %0" : "=a"(v) : "Nd"(port));
    return v;
}

static inline void outb(uint16_t port, uint8_t v)
{
    __asm__ __volatile__("outb %0, %1" : : "a"(v), "Nd"(port));
}

static void reset(void)
{
    __asm__ __volatile__("lidt %0\n\tint3" : : "m"(no_idt));
    for (;;)
        __asm__ __volatile__("hlt");
}

static uint8_t low8(uint32_t addr)
{
    uint8_t v;

    __asm__ __volatile__("movb (%1), %0" : "=q"(v) : "r"(addr) : "memory");
    return v;
}

static inline void outw(uint16_t port, uint16_t v)
{
    __asm__ __volatile__("outw %0, %1" : : "a"(v), "Nd"(port));
}

static inline uint16_t inw(uint16_t port)
{
    uint16_t v;

    __asm__ __volatile__("inw %1, %0" : "=a"(v) : "Nd"(port));
    return v;
}

static inline void outl(uint16_t port, uint32_t v)
{
    __asm__ __volatile__("outl %0, %1" : : "a"(v), "Nd"(port));
}

/* A pointer to absolute low memory (the BIOS data area), laundered through
 * asm as start/bios.h's peek and poke are: a compiler takes a dereference
 * of a constant near zero for a null-pointer bug and warns that it is one. */
static inline uint8_t *low_mem(uint32_t addr)
{
    uint8_t *p;

    __asm__("" : "=r"(p) : "0"((uintptr_t)addr));
    return p;
}

static inline uint32_t pci_read32(uint8_t bus, uint8_t dev, uint8_t fn,
                                  uint8_t reg)
{
    uint32_t v;

    __asm__ __volatile__("outl %0, %1" : : "a"(pci_addr(bus, dev, fn, reg)),
                         "Nd"((uint16_t)0xCF8));
    __asm__ __volatile__("inl %1, %0" : "=a"(v) : "Nd"((uint16_t)0xCFC));
    return v;
}

static inline void pci_write32(uint8_t bus, uint8_t dev, uint8_t fn,
                               uint8_t reg, uint32_t v)
{
    __asm__ __volatile__("outl %0, %1" : : "a"(pci_addr(bus, dev, fn, reg)),
                         "Nd"((uint16_t)0xCF8));
    __asm__ __volatile__("outl %0, %1" : : "a"(v), "Nd"((uint16_t)0xCFC));
}

static void lay_page(void)
{
    uint8_t *page = (uint8_t *)(uintptr_t)APT_ADDR;
    uint32_t halt = APT_ADDR + APT_OFF_HALT;
    uint32_t i;

    memcpy(page, aptramp_start, APT_BYTES);
    __asm__ volatile ("sgdt %0"
                      : "=m"(*(uint8_t (*)[6])(page + APT_OFF_GDTR)));
    for (i = 0; i < APT_IDT_GATES; i++) {
        uint8_t *g = page + APT_OFF_IDT + 8u * i;
    }
}

static int           g_x2;
static int           g_started;

static uint64_t rdmsr(uint32_t msr)
{
    uint32_t lo, hi;

    __asm__ volatile ("rdmsr" : "=a"(lo), "=d"(hi) : "c"(msr));
    return ((uint64_t)hi << 32) | lo;
}

static void wrmsr(uint32_t msr, uint64_t v)
{
    __asm__ volatile ("wrmsr" : : "c"(msr), "a"((uint32_t)v),
                      "d"((uint32_t)(v >> 32)) : "memory");
}

static inline uint8_t peek8(uint32_t addr)
{
    uint8_t v;

    __asm__ __volatile__("movb (%1), %0"
                         : "=q"(v) : "r"((uintptr_t)addr) : "memory");
    return v;
}

static inline uint16_t peek16(uint32_t addr)
{
    uint16_t v;

    __asm__ __volatile__("movw (%1), %0"
                         : "=r"(v) : "r"((uintptr_t)addr) : "memory");
    return v;
}

static inline void poke8(uint32_t addr, uint8_t v)
{
    __asm__ __volatile__("movb %0, (%1)"
                         : : "q"(v), "r"((uintptr_t)addr) : "memory");
}

static inline void poke16(uint32_t addr, uint16_t v)
{
    __asm__ __volatile__("movw %0, (%1)"
                         : : "r"(v), "r"((uintptr_t)addr) : "memory");
}

static inline uint32_t peek32(uint32_t addr)
{
    uint32_t v;

    __asm__ __volatile__("movl (%1), %0"
                         : "=r"(v) : "r"((uintptr_t)addr) : "memory");
    return v;
}

static inline void poke32(uint32_t addr, uint32_t v)
{
    __asm__ __volatile__("movl %0, (%1)"
                         : : "r"(v), "r"((uintptr_t)addr) : "memory");
}

static void enter_kernel(uint32_t entry, uint32_t zeropage, uint32_t from,
                         uint32_t move)
{
    memcpy((void *)(uintptr_t)LINUX_GDT_ADDR, g_gdt, sizeof g_gdt);
    g_gdtr.limit = (uint16_t)(sizeof g_gdt - 1u);
    g_gdtr.base = (uint32_t)LINUX_GDT_ADDR;

    if (move != 0) {
        memcpy((void *)(uintptr_t)LINUX_STUB_ADDR, linux_move_stub,
               (size_t)linux_move_stub_len);
        __asm__ __volatile__(
            "cli\n\t"
            "lgdt   %[gdtr]\n\t"
            "ljmp   $0x10, $1f\n"
            "1:\n\t"
            "movw   $0x18, %%ax\n\t"
            "movw   %%ax, %%ds\n\t"
            "movw   %%ax, %%es\n\t"
            "movw   %%ax, %%ss\n\t"
            "movw   %%ax, %%fs\n\t"
            "movw   %%ax, %%gs\n\t"
            "ljmp   $0x10, %[stub]\n\t"
            :
            : [gdtr] "m"(g_gdtr), [stub] "i"(LINUX_STUB_ADDR),
              "S"(from), "D"(entry), "c"(move), "d"(entry), "b"(zeropage)
            : "memory");
        for (;;)
            __asm__ __volatile__("hlt");
    }

    /* Nothing is declared clobbered because nothing comes back: after the
     * last jump this loader no longer exists, and the registers the
     * protocol names are set by hand. */
    __asm__ __volatile__(
        "cli\n\t"
        "lgdt   %[gdtr]\n\t"
        /* CS first, through a far jump to the next instruction, then the
         * data selectors: the protocol wants all four loaded from the
         * handoff GDT, not merely present in it. */
        "ljmp   $0x10, $1f\n"
        "1:\n\t"
        "movw   $0x18, %%ax\n\t"
        "movw   %%ax, %%ds\n\t"
        "movw   %%ax, %%es\n\t"
        "movw   %%ax, %%ss\n\t"
        "movw   %%ax, %%fs\n\t"
        "movw   %%ax, %%gs\n\t"
        "xorl   %%ebp, %%ebp\n\t"
        "xorl   %%edi, %%edi\n\t"
        "xorl   %%ebx, %%ebx\n\t"
        "jmp    *%[entry]\n\t"
        :
        : [gdtr] "m"(g_gdtr), [entry] "d"(entry), "S"(zeropage)
        : "memory");
    for (;;)
        __asm__ __volatile__("hlt");
}
