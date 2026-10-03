# 0 "falcon-bios/src/start/boot.S"
# 0 "<built-in>"
#define __STDC__ 1
# 0 "<built-in>"
#define __ASSEMBLER__ 1
# 0 "<built-in>"
#define __STDC_HOSTED__ 1
# 0 "<built-in>"
#define __STDC_EMBED_NOT_FOUND__ 0
# 0 "<built-in>"
#define __STDC_EMBED_FOUND__ 1
# 0 "<built-in>"
#define __STDC_EMBED_EMPTY__ 2
# 0 "<built-in>"
#define __GNUC__ 15
# 0 "<built-in>"
#define __GNUC_MINOR__ 3
# 0 "<built-in>"
#define __GNUC_PATCHLEVEL__ 0
# 0 "<built-in>"
#define __VERSION__ "15.3.0"
# 0 "<built-in>"
#define __ATOMIC_RELAXED 0
# 0 "<built-in>"
#define __ATOMIC_SEQ_CST 5
# 0 "<built-in>"
#define __ATOMIC_ACQUIRE 2
# 0 "<built-in>"
#define __ATOMIC_RELEASE 3
# 0 "<built-in>"
#define __ATOMIC_ACQ_REL 4
# 0 "<built-in>"
#define __ATOMIC_CONSUME 1
# 0 "<built-in>"
#define __pic__ 2
# 0 "<built-in>"
#define __PIC__ 2
# 0 "<built-in>"
#define __pie__ 2
# 0 "<built-in>"
#define __PIE__ 2
# 0 "<built-in>"
#define __FINITE_MATH_ONLY__ 0
# 0 "<built-in>"
#define __SIZEOF_INT__ 4
# 0 "<built-in>"
#define __SIZEOF_LONG__ 4
# 0 "<built-in>"
#define __SIZEOF_LONG_LONG__ 8
# 0 "<built-in>"
#define __SIZEOF_SHORT__ 2
# 0 "<built-in>"
#define __SIZEOF_FLOAT__ 4
# 0 "<built-in>"
#define __SIZEOF_DOUBLE__ 8
# 0 "<built-in>"
#define __SIZEOF_LONG_DOUBLE__ 12
# 0 "<built-in>"
#define __SIZEOF_SIZE_T__ 4
# 0 "<built-in>"
#define __CHAR_BIT__ 8
# 0 "<built-in>"
#define __BIGGEST_ALIGNMENT__ 16
# 0 "<built-in>"
#define __ORDER_LITTLE_ENDIAN__ 1234
# 0 "<built-in>"
#define __ORDER_BIG_ENDIAN__ 4321
# 0 "<built-in>"
#define __ORDER_PDP_ENDIAN__ 3412
# 0 "<built-in>"
#define __BYTE_ORDER__ __ORDER_LITTLE_ENDIAN__
# 0 "<built-in>"
#define __FLOAT_WORD_ORDER__ __ORDER_LITTLE_ENDIAN__
# 0 "<built-in>"
#define __SIZEOF_POINTER__ 4
# 0 "<built-in>"
#define __GNUC_EXECUTION_CHARSET_NAME "UTF-8"
# 0 "<built-in>"
#define __GNUC_WIDE_EXECUTION_CHARSET_NAME "UTF-32LE"
# 0 "<built-in>"
#define __SIZE_TYPE__ unsigned int
# 0 "<built-in>"
#define __PTRDIFF_TYPE__ int
# 0 "<built-in>"
#define __WCHAR_TYPE__ long int
# 0 "<built-in>"
#define __WINT_TYPE__ unsigned int
# 0 "<built-in>"
#define __INTMAX_TYPE__ long long int
# 0 "<built-in>"
#define __UINTMAX_TYPE__ long long unsigned int
# 0 "<built-in>"
#define __CHAR8_TYPE__ unsigned char
# 0 "<built-in>"
#define __CHAR16_TYPE__ short unsigned int
# 0 "<built-in>"
#define __CHAR32_TYPE__ unsigned int
# 0 "<built-in>"
#define __SIG_ATOMIC_TYPE__ int
# 0 "<built-in>"
#define __INT8_TYPE__ signed char
# 0 "<built-in>"
#define __INT16_TYPE__ short int
# 0 "<built-in>"
#define __INT32_TYPE__ int
# 0 "<built-in>"
#define __INT64_TYPE__ long long int
# 0 "<built-in>"
#define __UINT8_TYPE__ unsigned char
# 0 "<built-in>"
#define __UINT16_TYPE__ short unsigned int
# 0 "<built-in>"
#define __UINT32_TYPE__ unsigned int
# 0 "<built-in>"
#define __UINT64_TYPE__ long long unsigned int
# 0 "<built-in>"
#define __INT_LEAST8_TYPE__ signed char
# 0 "<built-in>"
#define __INT_LEAST16_TYPE__ short int
# 0 "<built-in>"
#define __INT_LEAST32_TYPE__ int
# 0 "<built-in>"
#define __INT_LEAST64_TYPE__ long long int
# 0 "<built-in>"
#define __UINT_LEAST8_TYPE__ unsigned char
# 0 "<built-in>"
#define __UINT_LEAST16_TYPE__ short unsigned int
# 0 "<built-in>"
#define __UINT_LEAST32_TYPE__ unsigned int
# 0 "<built-in>"
#define __UINT_LEAST64_TYPE__ long long unsigned int
# 0 "<built-in>"
#define __INT_FAST8_TYPE__ signed char
# 0 "<built-in>"
#define __INT_FAST16_TYPE__ int
# 0 "<built-in>"
#define __INT_FAST32_TYPE__ int
# 0 "<built-in>"
#define __INT_FAST64_TYPE__ long long int
# 0 "<built-in>"
#define __UINT_FAST8_TYPE__ unsigned char
# 0 "<built-in>"
#define __UINT_FAST16_TYPE__ unsigned int
# 0 "<built-in>"
#define __UINT_FAST32_TYPE__ unsigned int
# 0 "<built-in>"
#define __UINT_FAST64_TYPE__ long long unsigned int
# 0 "<built-in>"
#define __INTPTR_TYPE__ int
# 0 "<built-in>"
#define __UINTPTR_TYPE__ unsigned int
# 0 "<built-in>"
#define __GXX_ABI_VERSION 1020
# 0 "<built-in>"
#define __SCHAR_MAX__ 0x7f
# 0 "<built-in>"
#define __SHRT_MAX__ 0x7fff
# 0 "<built-in>"
#define __INT_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __LONG_MAX__ 0x7fffffffL
# 0 "<built-in>"
#define __LONG_LONG_MAX__ 0x7fffffffffffffffLL
# 0 "<built-in>"
#define __WCHAR_MAX__ 0x7fffffffL
# 0 "<built-in>"
#define __WCHAR_MIN__ (-__WCHAR_MAX__ - 1)
# 0 "<built-in>"
#define __WINT_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __WINT_MIN__ 0U
# 0 "<built-in>"
#define __PTRDIFF_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __SIZE_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __SCHAR_WIDTH__ 8
# 0 "<built-in>"
#define __SHRT_WIDTH__ 16
# 0 "<built-in>"
#define __INT_WIDTH__ 32
# 0 "<built-in>"
#define __LONG_WIDTH__ 32
# 0 "<built-in>"
#define __LONG_LONG_WIDTH__ 64
# 0 "<built-in>"
#define __WCHAR_WIDTH__ 32
# 0 "<built-in>"
#define __WINT_WIDTH__ 32
# 0 "<built-in>"
#define __PTRDIFF_WIDTH__ 32
# 0 "<built-in>"
#define __SIZE_WIDTH__ 32
# 0 "<built-in>"
#define __BITINT_MAXWIDTH__ 65535
# 0 "<built-in>"
#define __INTMAX_MAX__ 0x7fffffffffffffffLL
# 0 "<built-in>"
#define __INTMAX_C(c) c ## LL
# 0 "<built-in>"
#define __UINTMAX_MAX__ 0xffffffffffffffffULL
# 0 "<built-in>"
#define __UINTMAX_C(c) c ## ULL
# 0 "<built-in>"
#define __INTMAX_WIDTH__ 64
# 0 "<built-in>"
#define __SIG_ATOMIC_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __SIG_ATOMIC_MIN__ (-__SIG_ATOMIC_MAX__ - 1)
# 0 "<built-in>"
#define __SIG_ATOMIC_WIDTH__ 32
# 0 "<built-in>"
#define __INT8_MAX__ 0x7f
# 0 "<built-in>"
#define __INT16_MAX__ 0x7fff
# 0 "<built-in>"
#define __INT32_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __INT64_MAX__ 0x7fffffffffffffffLL
# 0 "<built-in>"
#define __UINT8_MAX__ 0xff
# 0 "<built-in>"
#define __UINT16_MAX__ 0xffff
# 0 "<built-in>"
#define __UINT32_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __UINT64_MAX__ 0xffffffffffffffffULL
# 0 "<built-in>"
#define __INT_LEAST8_MAX__ 0x7f
# 0 "<built-in>"
#define __INT8_C(c) c
# 0 "<built-in>"
#define __INT_LEAST8_WIDTH__ 8
# 0 "<built-in>"
#define __INT_LEAST16_MAX__ 0x7fff
# 0 "<built-in>"
#define __INT16_C(c) c
# 0 "<built-in>"
#define __INT_LEAST16_WIDTH__ 16
# 0 "<built-in>"
#define __INT_LEAST32_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __INT32_C(c) c
# 0 "<built-in>"
#define __INT_LEAST32_WIDTH__ 32
# 0 "<built-in>"
#define __INT_LEAST64_MAX__ 0x7fffffffffffffffLL
# 0 "<built-in>"
#define __INT64_C(c) c ## LL
# 0 "<built-in>"
#define __INT_LEAST64_WIDTH__ 64
# 0 "<built-in>"
#define __UINT_LEAST8_MAX__ 0xff
# 0 "<built-in>"
#define __UINT8_C(c) c
# 0 "<built-in>"
#define __UINT_LEAST16_MAX__ 0xffff
# 0 "<built-in>"
#define __UINT16_C(c) c
# 0 "<built-in>"
#define __UINT_LEAST32_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __UINT32_C(c) c ## U
# 0 "<built-in>"
#define __UINT_LEAST64_MAX__ 0xffffffffffffffffULL
# 0 "<built-in>"
#define __UINT64_C(c) c ## ULL
# 0 "<built-in>"
#define __INT_FAST8_MAX__ 0x7f
# 0 "<built-in>"
#define __INT_FAST8_WIDTH__ 8
# 0 "<built-in>"
#define __INT_FAST16_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __INT_FAST16_WIDTH__ 32
# 0 "<built-in>"
#define __INT_FAST32_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __INT_FAST32_WIDTH__ 32
# 0 "<built-in>"
#define __INT_FAST64_MAX__ 0x7fffffffffffffffLL
# 0 "<built-in>"
#define __INT_FAST64_WIDTH__ 64
# 0 "<built-in>"
#define __UINT_FAST8_MAX__ 0xff
# 0 "<built-in>"
#define __UINT_FAST16_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __UINT_FAST32_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __UINT_FAST64_MAX__ 0xffffffffffffffffULL
# 0 "<built-in>"
#define __INTPTR_MAX__ 0x7fffffff
# 0 "<built-in>"
#define __INTPTR_WIDTH__ 32
# 0 "<built-in>"
#define __UINTPTR_MAX__ 0xffffffffU
# 0 "<built-in>"
#define __GCC_IEC_559 2
# 0 "<built-in>"
#define __GCC_IEC_559_COMPLEX 2
# 0 "<built-in>"
#define __FLT_EVAL_METHOD__ 2
# 0 "<built-in>"
#define __FLT_EVAL_METHOD_TS_18661_3__ 2
# 0 "<built-in>"
#define __DEC_EVAL_METHOD__ 2
# 0 "<built-in>"
#define __FLT_RADIX__ 2
# 0 "<built-in>"
#define __FLT_MANT_DIG__ 24
# 0 "<built-in>"
#define __FLT_DIG__ 6
# 0 "<built-in>"
#define __FLT_MIN_EXP__ (-125)
# 0 "<built-in>"
#define __FLT_MIN_10_EXP__ (-37)
# 0 "<built-in>"
#define __FLT_MAX_EXP__ 128
# 0 "<built-in>"
#define __FLT_MAX_10_EXP__ 38
# 0 "<built-in>"
#define __FLT_DECIMAL_DIG__ 9
# 0 "<built-in>"
#define __FLT_MAX__ 3.40282346638528859811704183484516925e+38F
# 0 "<built-in>"
#define __FLT_NORM_MAX__ 3.40282346638528859811704183484516925e+38F
# 0 "<built-in>"
#define __FLT_MIN__ 1.17549435082228750796873653722224568e-38F
# 0 "<built-in>"
#define __FLT_EPSILON__ 1.19209289550781250000000000000000000e-7F
# 0 "<built-in>"
#define __FLT_DENORM_MIN__ 1.40129846432481707092372958328991613e-45F
# 0 "<built-in>"
#define __FLT_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __DBL_MANT_DIG__ 53
# 0 "<built-in>"
#define __DBL_DIG__ 15
# 0 "<built-in>"
#define __DBL_MIN_EXP__ (-1021)
# 0 "<built-in>"
#define __DBL_MIN_10_EXP__ (-307)
# 0 "<built-in>"
#define __DBL_MAX_EXP__ 1024
# 0 "<built-in>"
#define __DBL_MAX_10_EXP__ 308
# 0 "<built-in>"
#define __DBL_DECIMAL_DIG__ 17
# 0 "<built-in>"
#define __DBL_MAX__ ((double)1.79769313486231570814527423731704357e+308L)
# 0 "<built-in>"
#define __DBL_NORM_MAX__ ((double)1.79769313486231570814527423731704357e+308L)
# 0 "<built-in>"
#define __DBL_MIN__ ((double)2.22507385850720138309023271733240406e-308L)
# 0 "<built-in>"
#define __DBL_EPSILON__ ((double)2.22044604925031308084726333618164062e-16L)
# 0 "<built-in>"
#define __DBL_DENORM_MIN__ ((double)4.94065645841246544176568792868221372e-324L)
# 0 "<built-in>"
#define __DBL_HAS_DENORM__ 1
# 0 "<built-in>"
#define __DBL_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __DBL_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __DBL_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __LDBL_MANT_DIG__ 64
# 0 "<built-in>"
#define __LDBL_DIG__ 18
# 0 "<built-in>"
#define __LDBL_MIN_EXP__ (-16381)
# 0 "<built-in>"
#define __LDBL_MIN_10_EXP__ (-4931)
# 0 "<built-in>"
#define __LDBL_MAX_EXP__ 16384
# 0 "<built-in>"
#define __LDBL_MAX_10_EXP__ 4932
# 0 "<built-in>"
#define __DECIMAL_DIG__ 21
# 0 "<built-in>"
#define __LDBL_DECIMAL_DIG__ 21
# 0 "<built-in>"
#define __LDBL_MAX__ 1.18973149535723176502126385303097021e+4932L
# 0 "<built-in>"
#define __LDBL_NORM_MAX__ 1.18973149535723176502126385303097021e+4932L
# 0 "<built-in>"
#define __LDBL_MIN__ 3.36210314311209350626267781732175260e-4932L
# 0 "<built-in>"
#define __LDBL_EPSILON__ 1.08420217248550443400745280086994171e-19L
# 0 "<built-in>"
#define __LDBL_DENORM_MIN__ 3.64519953188247460252840593361941982e-4951L
# 0 "<built-in>"
#define __LDBL_HAS_DENORM__ 1
# 0 "<built-in>"
#define __LDBL_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __LDBL_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __LDBL_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __FLT16_MANT_DIG__ 11
# 0 "<built-in>"
#define __FLT16_DIG__ 3
# 0 "<built-in>"
#define __FLT16_MIN_EXP__ (-13)
# 0 "<built-in>"
#define __FLT16_MIN_10_EXP__ (-4)
# 0 "<built-in>"
#define __FLT16_MAX_EXP__ 16
# 0 "<built-in>"
#define __FLT16_MAX_10_EXP__ 4
# 0 "<built-in>"
#define __FLT16_DECIMAL_DIG__ 5
# 0 "<built-in>"
#define __FLT16_MAX__ 6.55040000000000000000000000000000000e+4F16
# 0 "<built-in>"
#define __FLT16_NORM_MAX__ 6.55040000000000000000000000000000000e+4F16
# 0 "<built-in>"
#define __FLT16_MIN__ 6.10351562500000000000000000000000000e-5F16
# 0 "<built-in>"
#define __FLT16_EPSILON__ 9.76562500000000000000000000000000000e-4F16
# 0 "<built-in>"
#define __FLT16_DENORM_MIN__ 5.96046447753906250000000000000000000e-8F16
# 0 "<built-in>"
#define __FLT16_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT16_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT16_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT16_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __FLT32_MANT_DIG__ 24
# 0 "<built-in>"
#define __FLT32_DIG__ 6
# 0 "<built-in>"
#define __FLT32_MIN_EXP__ (-125)
# 0 "<built-in>"
#define __FLT32_MIN_10_EXP__ (-37)
# 0 "<built-in>"
#define __FLT32_MAX_EXP__ 128
# 0 "<built-in>"
#define __FLT32_MAX_10_EXP__ 38
# 0 "<built-in>"
#define __FLT32_DECIMAL_DIG__ 9
# 0 "<built-in>"
#define __FLT32_MAX__ 3.40282346638528859811704183484516925e+38F32
# 0 "<built-in>"
#define __FLT32_NORM_MAX__ 3.40282346638528859811704183484516925e+38F32
# 0 "<built-in>"
#define __FLT32_MIN__ 1.17549435082228750796873653722224568e-38F32
# 0 "<built-in>"
#define __FLT32_EPSILON__ 1.19209289550781250000000000000000000e-7F32
# 0 "<built-in>"
#define __FLT32_DENORM_MIN__ 1.40129846432481707092372958328991613e-45F32
# 0 "<built-in>"
#define __FLT32_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT32_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT32_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT32_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __FLT64_MANT_DIG__ 53
# 0 "<built-in>"
#define __FLT64_DIG__ 15
# 0 "<built-in>"
#define __FLT64_MIN_EXP__ (-1021)
# 0 "<built-in>"
#define __FLT64_MIN_10_EXP__ (-307)
# 0 "<built-in>"
#define __FLT64_MAX_EXP__ 1024
# 0 "<built-in>"
#define __FLT64_MAX_10_EXP__ 308
# 0 "<built-in>"
#define __FLT64_DECIMAL_DIG__ 17
# 0 "<built-in>"
#define __FLT64_MAX__ 1.79769313486231570814527423731704357e+308F64
# 0 "<built-in>"
#define __FLT64_NORM_MAX__ 1.79769313486231570814527423731704357e+308F64
# 0 "<built-in>"
#define __FLT64_MIN__ 2.22507385850720138309023271733240406e-308F64
# 0 "<built-in>"
#define __FLT64_EPSILON__ 2.22044604925031308084726333618164062e-16F64
# 0 "<built-in>"
#define __FLT64_DENORM_MIN__ 4.94065645841246544176568792868221372e-324F64
# 0 "<built-in>"
#define __FLT64_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT64_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT64_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT64_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __FLT128_MANT_DIG__ 113
# 0 "<built-in>"
#define __FLT128_DIG__ 33
# 0 "<built-in>"
#define __FLT128_MIN_EXP__ (-16381)
# 0 "<built-in>"
#define __FLT128_MIN_10_EXP__ (-4931)
# 0 "<built-in>"
#define __FLT128_MAX_EXP__ 16384
# 0 "<built-in>"
#define __FLT128_MAX_10_EXP__ 4932
# 0 "<built-in>"
#define __FLT128_DECIMAL_DIG__ 36
# 0 "<built-in>"
#define __FLT128_MAX__ 1.18973149535723176508575932662800702e+4932F128
# 0 "<built-in>"
#define __FLT128_NORM_MAX__ 1.18973149535723176508575932662800702e+4932F128
# 0 "<built-in>"
#define __FLT128_MIN__ 3.36210314311209350626267781732175260e-4932F128
# 0 "<built-in>"
#define __FLT128_EPSILON__ 1.92592994438723585305597794258492732e-34F128
# 0 "<built-in>"
#define __FLT128_DENORM_MIN__ 6.47517511943802511092443895822764655e-4966F128
# 0 "<built-in>"
#define __FLT128_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT128_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT128_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT128_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __FLT32X_MANT_DIG__ 53
# 0 "<built-in>"
#define __FLT32X_DIG__ 15
# 0 "<built-in>"
#define __FLT32X_MIN_EXP__ (-1021)
# 0 "<built-in>"
#define __FLT32X_MIN_10_EXP__ (-307)
# 0 "<built-in>"
#define __FLT32X_MAX_EXP__ 1024
# 0 "<built-in>"
#define __FLT32X_MAX_10_EXP__ 308
# 0 "<built-in>"
#define __FLT32X_DECIMAL_DIG__ 17
# 0 "<built-in>"
#define __FLT32X_MAX__ 1.79769313486231570814527423731704357e+308F32x
# 0 "<built-in>"
#define __FLT32X_NORM_MAX__ 1.79769313486231570814527423731704357e+308F32x
# 0 "<built-in>"
#define __FLT32X_MIN__ 2.22507385850720138309023271733240406e-308F32x
# 0 "<built-in>"
#define __FLT32X_EPSILON__ 2.22044604925031308084726333618164062e-16F32x
# 0 "<built-in>"
#define __FLT32X_DENORM_MIN__ 4.94065645841246544176568792868221372e-324F32x
# 0 "<built-in>"
#define __FLT32X_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT32X_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT32X_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT32X_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __FLT64X_MANT_DIG__ 64
# 0 "<built-in>"
#define __FLT64X_DIG__ 18
# 0 "<built-in>"
#define __FLT64X_MIN_EXP__ (-16381)
# 0 "<built-in>"
#define __FLT64X_MIN_10_EXP__ (-4931)
# 0 "<built-in>"
#define __FLT64X_MAX_EXP__ 16384
# 0 "<built-in>"
#define __FLT64X_MAX_10_EXP__ 4932
# 0 "<built-in>"
#define __FLT64X_DECIMAL_DIG__ 21
# 0 "<built-in>"
#define __FLT64X_MAX__ 1.18973149535723176502126385303097021e+4932F64x
# 0 "<built-in>"
#define __FLT64X_NORM_MAX__ 1.18973149535723176502126385303097021e+4932F64x
# 0 "<built-in>"
#define __FLT64X_MIN__ 3.36210314311209350626267781732175260e-4932F64x
# 0 "<built-in>"
#define __FLT64X_EPSILON__ 1.08420217248550443400745280086994171e-19F64x
# 0 "<built-in>"
#define __FLT64X_DENORM_MIN__ 3.64519953188247460252840593361941982e-4951F64x
# 0 "<built-in>"
#define __FLT64X_HAS_DENORM__ 1
# 0 "<built-in>"
#define __FLT64X_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __FLT64X_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __FLT64X_IS_IEC_60559__ 1
# 0 "<built-in>"
#define __BFLT16_MANT_DIG__ 8
# 0 "<built-in>"
#define __BFLT16_DIG__ 2
# 0 "<built-in>"
#define __BFLT16_MIN_EXP__ (-125)
# 0 "<built-in>"
#define __BFLT16_MIN_10_EXP__ (-37)
# 0 "<built-in>"
#define __BFLT16_MAX_EXP__ 128
# 0 "<built-in>"
#define __BFLT16_MAX_10_EXP__ 38
# 0 "<built-in>"
#define __BFLT16_DECIMAL_DIG__ 4
# 0 "<built-in>"
#define __BFLT16_MAX__ 3.38953138925153547590470800371487867e+38BF16
# 0 "<built-in>"
#define __BFLT16_NORM_MAX__ 3.38953138925153547590470800371487867e+38BF16
# 0 "<built-in>"
#define __BFLT16_MIN__ 1.17549435082228750796873653722224568e-38BF16
# 0 "<built-in>"
#define __BFLT16_EPSILON__ 7.81250000000000000000000000000000000e-3BF16
# 0 "<built-in>"
#define __BFLT16_DENORM_MIN__ 9.18354961579912115600575419704879436e-41BF16
# 0 "<built-in>"
#define __BFLT16_HAS_DENORM__ 1
# 0 "<built-in>"
#define __BFLT16_HAS_INFINITY__ 1
# 0 "<built-in>"
#define __BFLT16_HAS_QUIET_NAN__ 1
# 0 "<built-in>"
#define __BFLT16_IS_IEC_60559__ 0
# 0 "<built-in>"
#define __DEC32_MANT_DIG__ 7
# 0 "<built-in>"
#define __DEC32_MIN_EXP__ (-94)
# 0 "<built-in>"
#define __DEC32_MAX_EXP__ 97
# 0 "<built-in>"
#define __DEC32_MIN__ 1E-95DF
# 0 "<built-in>"
#define __DEC32_MAX__ 9.999999E96DF
# 0 "<built-in>"
#define __DEC32_EPSILON__ 1E-6DF
# 0 "<built-in>"
#define __DEC32_SUBNORMAL_MIN__ 0.000001E-95DF
# 0 "<built-in>"
#define __DEC64_MANT_DIG__ 16
# 0 "<built-in>"
#define __DEC64_MIN_EXP__ (-382)
# 0 "<built-in>"
#define __DEC64_MAX_EXP__ 385
# 0 "<built-in>"
#define __DEC64_MIN__ 1E-383DD
# 0 "<built-in>"
#define __DEC64_MAX__ 9.999999999999999E384DD
# 0 "<built-in>"
#define __DEC64_EPSILON__ 1E-15DD
# 0 "<built-in>"
#define __DEC64_SUBNORMAL_MIN__ 0.000000000000001E-383DD
# 0 "<built-in>"
#define __DEC128_MANT_DIG__ 34
# 0 "<built-in>"
#define __DEC128_MIN_EXP__ (-6142)
# 0 "<built-in>"
#define __DEC128_MAX_EXP__ 6145
# 0 "<built-in>"
#define __DEC128_MIN__ 1E-6143DL
# 0 "<built-in>"
#define __DEC128_MAX__ 9.999999999999999999999999999999999E6144DL
# 0 "<built-in>"
#define __DEC128_EPSILON__ 1E-33DL
# 0 "<built-in>"
#define __DEC128_SUBNORMAL_MIN__ 0.000000000000000000000000000000001E-6143DL
# 0 "<built-in>"
#define __DEC64X_MANT_DIG__ 34
# 0 "<built-in>"
#define __DEC64X_MIN_EXP__ (-6142)
# 0 "<built-in>"
#define __DEC64X_MAX_EXP__ 6145
# 0 "<built-in>"
#define __DEC64X_MIN__ 1E-6143D64x
# 0 "<built-in>"
#define __DEC64X_MAX__ 9.999999999999999999999999999999999E6144D64x
# 0 "<built-in>"
#define __DEC64X_EPSILON__ 1E-33D64x
# 0 "<built-in>"
#define __DEC64X_SUBNORMAL_MIN__ 0.000000000000000000000000000000001E-6143D64x
# 0 "<built-in>"
#define __REGISTER_PREFIX__ 
# 0 "<built-in>"
#define __USER_LABEL_PREFIX__ 
# 0 "<built-in>"
#define __GNUC_STDC_INLINE__ 1
# 0 "<built-in>"
#define __NO_INLINE__ 1
# 0 "<built-in>"
#define __GCC_HAVE_SYNC_COMPARE_AND_SWAP_1 1
# 0 "<built-in>"
#define __GCC_HAVE_SYNC_COMPARE_AND_SWAP_2 1
# 0 "<built-in>"
#define __GCC_HAVE_SYNC_COMPARE_AND_SWAP_4 1
# 0 "<built-in>"
#define __GCC_HAVE_SYNC_COMPARE_AND_SWAP_8 1
# 0 "<built-in>"
#define __GCC_ATOMIC_BOOL_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_CHAR_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_CHAR8_T_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_CHAR16_T_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_CHAR32_T_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_WCHAR_T_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_SHORT_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_INT_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_LONG_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_LLONG_LOCK_FREE 2
# 0 "<built-in>"
#define __GCC_ATOMIC_TEST_AND_SET_TRUEVAL 1
# 0 "<built-in>"
#define __GCC_DESTRUCTIVE_SIZE 64
# 0 "<built-in>"
#define __GCC_CONSTRUCTIVE_SIZE 64
# 0 "<built-in>"
#define __GCC_ATOMIC_POINTER_LOCK_FREE 2
# 0 "<built-in>"
#define __HAVE_SPECULATION_SAFE_VALUE 1
# 0 "<built-in>"
#define __GCC_HAVE_DWARF2_CFI_ASM 1
# 0 "<built-in>"
#define __PRAGMA_REDEFINE_EXTNAME 1
# 0 "<built-in>"
#define __SSP_STRONG__ 3
# 0 "<built-in>"
#define __SIZEOF_WCHAR_T__ 4
# 0 "<built-in>"
#define __SIZEOF_WINT_T__ 4
# 0 "<built-in>"
#define __SIZEOF_PTRDIFF_T__ 4
# 0 "<built-in>"
#define __i386 1
# 0 "<built-in>"
#define __i386__ 1
# 0 "<built-in>"
#define i386 1
# 0 "<built-in>"
#define _ILP32 1
# 0 "<built-in>"
#define __ILP32__ 1
# 0 "<built-in>"
#define __SIZEOF_FLOAT80__ 12
# 0 "<built-in>"
#define __SIZEOF_FLOAT128__ 16
# 0 "<built-in>"
#define __ATOMIC_HLE_ACQUIRE 65536
# 0 "<built-in>"
#define __ATOMIC_HLE_RELEASE 131072
# 0 "<built-in>"
#define __GCC_ASM_FLAG_OUTPUTS__ 1
# 0 "<built-in>"
#define __k8 1
# 0 "<built-in>"
#define __k8__ 1
# 0 "<built-in>"
#define __code_model_32__ 1
# 0 "<built-in>"
#define __MMX__ 1
# 0 "<built-in>"
#define __SSE__ 1
# 0 "<built-in>"
#define __SSE2__ 1
# 0 "<built-in>"
#define __FXSR__ 1
# 0 "<built-in>"
#define __LAHF_SAHF__ 1
# 0 "<built-in>"
#define __SEG_FS 1
# 0 "<built-in>"
#define __SEG_GS 1
# 0 "<built-in>"
#define __gnu_linux__ 1
# 0 "<built-in>"
#define __linux 1
# 0 "<built-in>"
#define __linux__ 1
# 0 "<built-in>"
#define linux 1
# 0 "<built-in>"
#define __unix 1
# 0 "<built-in>"
#define __unix__ 1
# 0 "<built-in>"
#define unix 1
# 0 "<built-in>"
#define __ELF__ 1
# 0 "<built-in>"
#define __DECIMAL_BID_FORMAT__ 1
# 0 "<command-line>"
# 1 "/usr/include/stdc-predef.h" 1 3 4
# 19 "/usr/include/stdc-predef.h" 3 4
#define _STDC_PREDEF_H 1
# 38 "/usr/include/stdc-predef.h" 3 4
#define __STDC_IEC_559__ 1
#define __STDC_IEC_60559_BFP__ 201404L
# 48 "/usr/include/stdc-predef.h" 3 4
#define __STDC_IEC_559_COMPLEX__ 1
#define __STDC_IEC_60559_COMPLEX__ 201404L
# 62 "/usr/include/stdc-predef.h" 3 4
#define __STDC_ISO_10646__ 201706L
# 0 "<command-line>" 2
# 1 "falcon-bios/src/start/boot.S"
# 39 "falcon-bios/src/start/boot.S"
# 1 "falcon-bios/src/start/biosdefs.h" 1
# 15 "falcon-bios/src/start/biosdefs.h"
#define FALCON_BIOSDEFS_H 



#define STAGE1_LOAD 0x7C00
# 34 "falcon-bios/src/start/biosdefs.h"
#define STAGE2_LOAD 0x7E00
# 51 "falcon-bios/src/start/biosdefs.h"
#define STAGE2_LBA 34
#define STAGE2_SECTORS 64



#define STAGE2_MBR_MAX_SECTORS 62



#define FD_S2_SECTORS_OFF 508





#define FD_STAGE3_SECTOR 98
# 76 "falcon-bios/src/start/biosdefs.h"
#define STAGE1_S2_LBA_OFF 0x1B2
#define STAGE1_S2_SECTORS_OFF 0x1B6
#define STAGE2_LBA_MBR 1






#define BOOTPART_MARKER_SECTOR 8
#define BOOTPART_STAGE3_SECTOR 32





#define STAGE2_MAGIC 0x32534F42
# 106 "falcon-bios/src/start/biosdefs.h"
#define ADDR_END 0x100000000ull





#define SEL_CODE32 0x08
#define SEL_DATA32 0x10
#define SEL_CODE16 0x18
#define SEL_DATA16 0x20
# 132 "falcon-bios/src/start/biosdefs.h"
#define GATE_ADDR STAGE2_LOAD
#define GATE_MAGIC (GATE_ADDR + 0x04)
#define GATE_THUNK (GATE_ADDR + 0x08)
#define GATE_BOOT16 (GATE_ADDR + 0x10)
#define GATE_BOOTPART (GATE_ADDR + 0x18)

#define GATE_REGS (GATE_ADDR + 0x20)
#define GATE_VECTOR (GATE_ADDR + 0x40)
#define GATE_DRIVE (GATE_ADDR + 0x41)
#define GATE_MARKER (GATE_ADDR + 0x42)

#define GATE_SI (GATE_ADDR + 0x44)


#define GATE_TARGET (GATE_ADDR + 0x48)
#define GATE_S2LEN (GATE_ADDR + 0x4C)
# 163 "falcon-bios/src/start/biosdefs.h"
#define BIOS_SCRATCH 0x00001000
#define DAP_ADDR (BIOS_SCRATCH + 0x00)
#define DRIVEPARM_ADDR (BIOS_SCRATCH + 0x40)
#define E820_ADDR (BIOS_SCRATCH + 0x80)
# 177 "falcon-bios/src/start/biosdefs.h"
#define ZEROPAGE_ADDR 0x00010000
#define CMDLINE_ADDR 0x00020000
#define KERNEL_ADDR 0x01000000







#define LINUX_STUB_ADDR 0x00003000
#define LINUX_GDT_ADDR 0x00003100

#define STAGE3_LOAD 0x100000
# 200 "falcon-bios/src/start/biosdefs.h"
#define APT_ADDR 0x00005000
#define APT_VECTOR (APT_ADDR >> 12)
#define APT_OFF_GDTR 0x08
#define APT_OFF_IDTR 0x10
#define APT_OFF_ENTRY 0x18
#define APT_OFF_STACKS 0x1C
#define APT_OFF_STACKB 0x20
#define APT_OFF_NSTACK 0x24
#define APT_OFF_TICKET 0x28
#define APT_OFF_IDT 0x40
#define APT_IDT_GATES 32
#define APT_OFF_HALT 0x140
#define APT_OFF_CODE 0x150
#define APT_BYTES 0x400
# 227 "falcon-bios/src/start/biosdefs.h"
#define STAGE2_STACK STAGE3_STACK
#define STAGE2_SCRATCH GZ_STAGE_ADDR
# 237 "falcon-bios/src/start/biosdefs.h"
#define BOUNCE_ADDR 0x90000
#define BOUNCE_BYTES 0x8000







#define TRACK_CACHE_ADDR 0x88000
#define TRACK_CACHE_BYTES 0x8000



#define STAGE3_MAGIC 0x33534F42
#define STAGE3_HDR 16
# 262 "falcon-bios/src/start/biosdefs.h"
#define STAGE3_MAGIC_GZ 0x5A534F42
#define GZ_STAGE_ADDR 0x00600000
# 277 "falcon-bios/src/start/biosdefs.h"
#define SMH_OFF_NEXT 19
#define SMH_BYTES 27





.macro safe_mbr_hook oldint13
 jmp 1f
 nop
 .ascii "$INT13SF"
 .ascii "FALCON  "
 .org \oldint13
 .long 0
 .long 0
1:
.endm
# 308 "falcon-bios/src/start/biosdefs.h"
#define VCD_OFF_DRIVE 0x0004
#define VCD_OFF_PHYS 0x0005
#define VCD_OFF_NRUNS 0x0006
#define VCD_OFF_PERCD 0x0008
#define VCD_OFF_NCD 0x000A
#define VCD_OFF_NOVER 0x000E
#define VCD_OFF_NREAL 0x0028
#define VCD_OFF_NLIMIT 0x002C
#define VCD_OFF_DAP 0x0010
#define VCD_OFF_END 0x0020
#define VCD_OFF_GAP 0x0024

#define VCD_OFF_OVER 0x0030
#define VCD_OV_LEN 16
#define VCD_OV_EXT 1
#define VCD_MAX_OVER_ASM 8



#define VCD_OFF_MOVEGDT 0x00B0





#define VCD_OFF_OLDINT15 0x00E0
#define VCD_OFF_NE820 0x00E4
#define VCD_OFF_E801CAP 0x00E6
#define VCD_OFF_E801LOW 0x00E8
#define VCD_OFF_RUNS 0x0100
#define VCD_RUN_LEN 16
#define VCD_MAX_RUNS_ASM 64
#define VCD_OFF_SPEC 0x0500







#define VCD_OFF_LSRC 0x0520
#define VCD_OFF_LDST 0x0522
#define VCD_OFF_LLEN 0x0524
#define VCD_OFF_LDRIVE 0x0526
#define VCD_OFF_LAUNCH 0x0530
#define VCD_OFF_CODE 0x0570

#define VCD_OFF_OLDINT13 (VCD_OFF_CODE + SMH_OFF_NEXT)







#define VCD_OFF_CODE15 0x0A00
#define VCD_OFF_E820 0x0B00
#define VCD_E820_LEN 20
#define VCD_E820_MAX 64





#define VCD_OVER_OFF 0x1000
#define VCD_OVER_MAX_CD 6
#define VCD_BYTES 0x4000
#define VCD_CD_BYTES 2048

#define VCD_STAGE_ADDR 0x00020000
# 387 "falcon-bios/src/start/biosdefs.h"
#define SWAP_OFF_DRIVE 0x0004
#define SWAP_OFF_CODE 0x0010

#define SWAP_OFF_OLDINT13 (SWAP_OFF_CODE + SMH_OFF_NEXT)

#define SWAP_BYTES 0x0400
# 408 "falcon-bios/src/start/biosdefs.h"
#define VFD_OFF_DRIVE 0x0008
#define VFD_OFF_HEADS 0x0009
#define VFD_OFF_SECTORS 0x000A
#define VFD_OFF_BTYPE 0x000B
#define VFD_OFF_CYLS 0x000C
#define VFD_OFF_TOTAL 0x0010
#define VFD_OFF_PHYS 0x0014
#define VFD_OFF_STATUS 0x001C
#define VFD_OFF_SRC 0x0020
#define VFD_OFF_DST 0x0024
#define VFD_OFF_CNT 0x0028
#define VFD_OFF_MOVEGDT 0x0040
#define VFD_OFF_DBT 0x0070
#define VFD_OFF_LDRIVE 0x007C
#define VFD_OFF_LAUNCH 0x0080
#define VFD_OFF_CODE 0x00C0

#define VFD_OFF_OLDINT13 (VFD_OFF_CODE + SMH_OFF_NEXT)





#define VFD_STAGE_ADDR VCD_STAGE_ADDR
# 443 "falcon-bios/src/start/biosdefs.h"
#define JMOD_REGION 0x00F00000
#define JMOD_SLOT_BYTES 0x00020000
#define JMOD_SLOTS 8
#define JMOD_REGION_END (JMOD_REGION + JMOD_SLOTS * JMOD_SLOT_BYTES)
#define JMOD_STACK_BYTES 0x2000
#define JMOD_SLOT_ADDR(n) (JMOD_REGION + (n) * JMOD_SLOT_BYTES)
#define JMOD_SLOT_ATA 0
#define JMOD_SLOT_UHCI 1
#define JMOD_SLOT_OHCI 2
#define JMOD_SLOT_EHCI 3
#define JMOD_SLOT_USB 4
#define JMOD_SLOT_PCC 5






#define JMOD_GZ_STAGE GZ_STAGE_ADDR
# 471 "falcon-bios/src/start/biosdefs.h"
#define JRES_OFF_DRIVE 0x04
#define JRES_OFF_A20 0x05
#define JRES_OFF_GDTR 0x08
#define JRES_OFF_SAVEGDT 0x10
#define JRES_OFF_SAVESP 0x18
#define JRES_OFF_SAVESS 0x1A
#define JRES_OFF_ENTRY 0x1C
#define JRES_OFF_STACK 0x20
#define JRES_OFF_PM32PTR 0x28
#define JRES_OFF_RMPTR 0x30
#define JRES_OFF_PM16PTR 0x34
#define JRES_OFF_NE820 0x3A
#define JRES_OFF_E801LOW 0x3C
#define JRES_OFF_E801CAP 0x3E
#define JRES_OFF_GDT 0x40
#define JRES_OFF_OLDINT15 0x68
#define JRES_OFF_REGS 0x70
#define JRES_OFF_CODE 0x90

#define JRES_OFF_OLDINT13 (JRES_OFF_CODE + SMH_OFF_NEXT)



#define JRES_OFF_CODE16 0x240



#define JRES_OFF_CODE15 0x300
#define JRES_OFF_E820 0x400
#define JRES_E820_MAX 64




#define JRES_OFF_RESET 0x900





#define JRES_OFF_MAGIC 0xB00
#define JRES_OFF_OLDINT18 0xB04
#define JRES_OFF_OLDINT19 0xB08
#define JRES_OFF_OLDKB 0xB0C
#define JRES_OFF_OLDNDISK 0xB0E
#define JRES_OFF_OLDINT16 0xB10
#define JRES_OFF_NKBD 0xB14
#define JRES_OFF_CALL 0xB15
#define JRES_OFF_KBD 0xB18
#define JRES_KBD_MAX 3
#define JRES_MAGIC 0x53524A46u
#define JRES_BYTES 0xC00




#define STAGE3_STACK 0x00800000
# 539 "falcon-bios/src/start/biosdefs.h"
#define JNL_REGION STAGE3_STACK
#define JNL_REGION_END JMOD_REGION






#define SERIAL_PORT 0x3F8
#define SERIAL_DIVISOR 1





#define SERIAL_SPINS 65536
# 40 "falcon-bios/src/start/boot.S" 2


#define DAP_CNT 2
#define DAP_OFF 4
#define DAP_LBA 8
#define DAP_SPT 16
#define DAP_HEADS 20

 .code16
 .section .text.stage1, "ax"
 .globl _start
 .globl s2_lba
 .globl s2_sectors
 .globl stage1_end

_start:



 cli
 xorw %ax, %ax
 movw %ax, %ds
 movw %ax, %es
 movw %ax, %ss
 movw $0x7C00, %sp
 cld
 sti

 movb %dl, boot_drive






 call serial_init



 movw $msg_notstage2, %si
 cmpw $0, s2_sectors
 je fail





 movw $dap, %si
 movl s2_lba, %eax
 movl %eax, 8(%si)
 movw $0x7E00, 4(%si)


 movb $0x41, %ah
 movw $0x55AA, %bx
 movb boot_drive, %dl
 int $0x13
 jc 1f
 cmpw $0xAA55, %bx
 jne 1f
 testb $1, %cl
 jnz read
1: movb $0x08, %ah
 movb boot_drive, %dl
 xorw %di, %di
 int $0x13
 movw $msg_readfail, %si
 jc fail
 xorw %ax, %ax
 movw %ax, %es
 movw $dap, %si
 andb $0x3F, %cl
 movb %cl, 16(%si)
 movb %dh, 20(%si)
 incw 20(%si)
# 126 "falcon-bios/src/start/boot.S"
read:
 movw $3, %bp
next:
 movw s2_sectors, %ax
 cmpb $0, 16(%si)
 je 2f
 movb $1, %al
 movw %ax, 2(%si)
 movl 8(%si), %eax
 xorl %edx, %edx
 divl 16(%si)
 incw %dx
 movb %dl, %cl
 xorl %edx, %edx
 divl 20(%si)
 movb %dl, %dh
 movb %al, %ch
 shlb $6, %ah
 orb %ah, %cl
 movw 4(%si), %bx
 movw $0x0201, %ax
 jmp 4f
2: movw %ax, 2(%si)
 movb $0x42, %ah
4: movb boot_drive, %dl
 int $0x13
 jnc 5f
 xorb %ah, %ah
 movb boot_drive, %dl
 int $0x13
 decw %bp
 jnz next
 movw $msg_readfail, %si
 jmp fail



5: movzwl 2(%si), %eax
 addl %eax, 8(%si)
 shlw $9, %ax
 addw %ax, 4(%si)
 movw 2(%si), %ax
 subw %ax, s2_sectors
 jnz read


 cmpl $0x32534F42, ((0x7E00 + 0x04))
 movw $msg_notstage2, %si
 jne fail


 movb boot_drive, %dl
 ljmp $0, $0x7E00



fail:
 call puts
 movw $msg_key, %si
 call puts
 xorb %ah, %ah
 int $0x16
 ljmp $0xFFFF, $0x0000




puts:
 lodsb
 testb %al, %al
 jz 2f
 pushw %ax
 movb $0x0E, %ah
 movw $0x0007, %bx
 int $0x10
 popw %bx
 movw $0xFFFF, %cx
 movw $(0x3F8 + 5), %dx
1: inb %dx, %al
 testb $0x20, %al
 jnz 3f
 loop 1b
3: movw $0x3F8, %dx
 movb %bl, %al
 outb %al, %dx
 jmp puts
2: ret







serial_init:
 movw $serial_tab, %si
 movw $6, %cx
 movb $(0x3F8 >> 8), %dh
1: lodsb
 movb %al, %dl
 lodsb
 outb %al, %dx
 loop 1b
 ret

serial_tab:
 .byte (0x3F8 + 1) & 0xFF, 0x00
 .byte (0x3F8 + 3) & 0xFF, 0x80
 .byte 0x3F8 & 0xFF, 1
 .byte (0x3F8 + 1) & 0xFF, 0x00
 .byte (0x3F8 + 3) & 0xFF, 0x03
 .byte (0x3F8 + 2) & 0xFF, 0xC7

boot_drive:
 .byte 0





dap:
 .byte 16
 .byte 0
 .word 0
 .word 0
 .word 0
 .quad 0
 .long 0
 .long 0

msg_readfail:
 .asciz "falcon-bios: read error\r\n"
msg_notstage2:
 .asciz "falcon-bios: no stage 2\r\n"
msg_key:
 .asciz "press a key\r\n"

stage1_end:






 .org _start + 0x1B2
s2_lba:
 .long 0
 .org _start + 0x1B6
s2_sectors:
 .word 0




 .org _start + 440





 . = _start + 510
 .byte 0x55, 0xAA
