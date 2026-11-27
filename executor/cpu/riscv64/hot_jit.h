// 页级 x86-64 翻译器。只服务于宿主 x86-64 的 Linux CGO 构建；其他
// 宿主继续使用逐指令解释路径。生成代码位于宿主私有映射:先以 RW
// 写入,再 mprotect 为 RX,不向客体暴露可执行数据页。

static uint32_t fp_flags_from_host(int exceptions) {
	uint32_t flags = 0;
	if ((exceptions & FE_INEXACT) != 0) {
		flags |= 1;
	}
	if ((exceptions & FE_UNDERFLOW) != 0) {
		flags |= 2;
	}
	if ((exceptions & FE_OVERFLOW) != 0) {
		flags |= 4;
	}
	if ((exceptions & FE_DIVBYZERO) != 0) {
		flags |= 8;
	}
	if ((exceptions & FE_INVALID) != 0) {
		flags |= 16;
	}
	return flags;
}

static bool fp_rounding_supported(uint32_t rm, uint64_t *fcsr) {
	if (rm == 0) {
		return true;
	}
	return rm == 7 && fcsr != nullptr && ((*fcsr >> 5) & 0x7) == 0;
}

static uint64_t fp_spec(uint32_t operation, uint8_t rd, uint8_t rs1, uint8_t rs2,
                        bool single, uint32_t rm, uint32_t selector) {
	return static_cast<uint64_t>(operation) | static_cast<uint64_t>(rd) << 8 |
	       static_cast<uint64_t>(rs1) << 13 | static_cast<uint64_t>(rs2) << 18 |
	       static_cast<uint64_t>(single ? 1 : 0) << 23 |
	       static_cast<uint64_t>(rm) << 24 | static_cast<uint64_t>(selector) << 27;
}

static bool fp_execute(uint64_t *registers, uint64_t *floats, uint64_t *fcsr,
                       uint64_t encoded, const uint64_t *int_source) {
	if (registers == nullptr || floats == nullptr || fcsr == nullptr) {
		return false;
	}
	const uint32_t operation = static_cast<uint32_t>(encoded) & 0xFF;
	const uint32_t rd = static_cast<uint32_t>((encoded >> 8) & 0x1F);
	const uint32_t rs1 = static_cast<uint32_t>((encoded >> 13) & 0x1F);
	const uint32_t rs2 = static_cast<uint32_t>((encoded >> 18) & 0x1F);
	const bool single = ((encoded >> 23) & 1) != 0;
	const uint32_t rm = static_cast<uint32_t>((encoded >> 24) & 0x7);
	const uint32_t selector = static_cast<uint32_t>((encoded >> 27) & 0x3);
	if (!fp_rounding_supported(rm, fcsr)) {
		return false;
	}

	if (operation == op_fcvt_from_int) {
		// Both 32-bit integer formats are exactly representable in binary64.
		if (!single && selector < 2) {
			const uint64_t source = rs1 == 0 ? 0 : int_source ? *int_source : registers[rs1];
			const double value = selector == 0 ? static_cast<double>(static_cast<int32_t>(source))
			                                   : static_cast<double>(static_cast<uint32_t>(source));
			std::memcpy(&floats[rd], &value, sizeof(value));
			return true;
		}
		const int previous_round = std::fegetround();
		if (previous_round != FE_TONEAREST) {
			std::fesetround(FE_TONEAREST);
		}
		std::feclearexcept(FE_ALL_EXCEPT);
		if (single) {
			float value = 0;
			const uint64_t source = rs1 == 0 ? 0
			                                : int_source != nullptr ? *int_source
			                                                        : registers[rs1];
			switch (selector) {
			case 0:
				value = static_cast<float>(static_cast<int32_t>(source));
				break;
			case 1:
				value = static_cast<float>(static_cast<uint32_t>(source));
				break;
			case 2:
				value = static_cast<float>(static_cast<int64_t>(source));
				break;
			default:
				value = static_cast<float>(source);
				break;
			}
			uint32_t bits = 0;
			std::memcpy(&bits, &value, sizeof(bits));
			floats[rd] = UINT64_C(0xFFFFFFFF00000000) | bits;
		} else {
			double value = 0;
			const uint64_t source = rs1 == 0 ? 0
			                                : int_source != nullptr ? *int_source
			                                                        : registers[rs1];
			switch (selector) {
			case 0:
				value = static_cast<double>(static_cast<int32_t>(source));
				break;
			case 1:
				value = static_cast<double>(static_cast<uint32_t>(source));
				break;
			case 2:
				value = static_cast<double>(static_cast<int64_t>(source));
				break;
			default:
				value = static_cast<double>(source);
				break;
			}
			std::memcpy(&floats[rd], &value, sizeof(value));
		}
		*fcsr |= fp_flags_from_host(std::fetestexcept(FE_ALL_EXCEPT));
		if (previous_round != FE_TONEAREST) {
			std::fesetround(previous_round);
		}
		return true;
	}

	uint64_t left_bits = single ? static_cast<uint32_t>(floats[rs1]) : floats[rs1];
	uint64_t right_bits = single ? static_cast<uint32_t>(floats[rs2]) : floats[rs2];
	const uint32_t exponent_bits = single ? 8 : 11;
	const uint32_t fraction_bits = single ? 23 : 52;
	const uint64_t exponent_mask = (UINT64_C(1) << exponent_bits) - 1;
	const uint64_t sign_shift = single ? 31 : 63;
	const uint64_t exponent_shift = fraction_bits;
	const uint64_t exponent =
	    (left_bits >> exponent_shift) & exponent_mask;
	const uint64_t right_exponent =
	    (right_bits >> exponent_shift) & exponent_mask;
	if (exponent == exponent_mask || right_exponent == exponent_mask) {
		return false;
	}

	if (operation == op_feq || operation == op_flt || operation == op_fle) {
		bool result = false;
		if (single) {
			float left = 0;
			float right = 0;
			const uint32_t left_word = static_cast<uint32_t>(left_bits);
			const uint32_t right_word = static_cast<uint32_t>(right_bits);
			std::memcpy(&left, &left_word, sizeof(left));
			std::memcpy(&right, &right_word, sizeof(right));
			result = operation == op_feq ? left == right
			                              : operation == op_flt ? left < right
			                                                    : left <= right;
		} else {
			double left = 0;
			double right = 0;
			std::memcpy(&left, &left_bits, sizeof(left));
			std::memcpy(&right, &right_bits, sizeof(right));
			result = operation == op_feq ? left == right
			                              : operation == op_flt ? left < right
			                                                    : left <= right;
		}
		if (rd != 0) {
			registers[rd] = result ? 1 : 0;
		}
		return true;
	}

	// A positive normal square root can only raise NX. Adding positive
	// normals below the largest finite exponent also cannot overflow or
	// underflow. Once NX is sticky, neither operation needs a flag probe.
	if (!single && (*fcsr & 1) && (left_bits >> 63) == 0 && exponent != 0 &&
	    (operation == op_fsqrt ||
	     (operation == op_fadd && (right_bits >> 63) == 0 && right_exponent != 0 &&
	      exponent < exponent_mask - 1 && right_exponent < exponent_mask - 1)) &&
	    std::fegetround() == FE_TONEAREST) {
		double left, right;
		std::memcpy(&left, &left_bits, sizeof(left));
		std::memcpy(&right, &right_bits, sizeof(right));
		const double result = operation == op_fsqrt ? std::sqrt(left) : left + right;
		std::memcpy(&floats[rd], &result, sizeof(result));
		return true;
	}

	if (operation == op_fsqrt) {
		const bool negative = ((left_bits >> sign_shift) & 1) != 0;
		const uint64_t fraction_mask = (UINT64_C(1) << fraction_bits) - 1;
		if (negative && (left_bits & fraction_mask) != 0) {
			return false;
		}
	}

	const int previous_round = std::fegetround();
	if (previous_round != FE_TONEAREST) {
		std::fesetround(FE_TONEAREST);
	}
	std::feclearexcept(FE_ALL_EXCEPT);
	if (single) {
		float left = 0;
		float right = 0;
		float value = 0;
		const uint32_t left_word = static_cast<uint32_t>(left_bits);
		const uint32_t right_word = static_cast<uint32_t>(right_bits);
		std::memcpy(&left, &left_word, sizeof(left));
		std::memcpy(&right, &right_word, sizeof(right));
		switch (operation) {
		case op_fadd:
			value = left + right;
			break;
		case op_fsub:
			value = left - right;
			break;
		case op_fmul:
			value = left * right;
			break;
		case op_fdiv:
			value = left / right;
			break;
		case op_fsqrt:
			value = std::sqrt(left);
			break;
		default:
			if (previous_round != FE_TONEAREST) {
				std::fesetround(previous_round);
			}
			return false;
		}
		uint32_t bits = 0;
		std::memcpy(&bits, &value, sizeof(bits));
		floats[rd] = UINT64_C(0xFFFFFFFF00000000) | bits;
	} else {
		double left = 0;
		double right = 0;
		double value = 0;
		std::memcpy(&left, &left_bits, sizeof(left));
		std::memcpy(&right, &right_bits, sizeof(right));
		switch (operation) {
		case op_fadd:
			value = left + right;
			break;
		case op_fsub:
			value = left - right;
			break;
		case op_fmul:
			value = left * right;
			break;
		case op_fdiv:
			value = left / right;
			break;
		case op_fsqrt:
			value = std::sqrt(left);
			break;
		default:
			if (previous_round != FE_TONEAREST) {
				std::fesetround(previous_round);
			}
			return false;
		}
		std::memcpy(&floats[rd], &value, sizeof(value));
	}
	*fcsr |= fp_flags_from_host(std::fetestexcept(FE_ALL_EXCEPT));
	if (previous_round != FE_TONEAREST) {
		std::fesetround(previous_round);
	}
	return true;
}

static float fp_read_single(const uint64_t *floats, uint32_t index) {
	const uint32_t bits = static_cast<uint32_t>(floats[index]);
	float value = 0;
	std::memcpy(&value, &bits, sizeof(value));
	return value;
}

static double fp_read_double(const uint64_t *floats, uint32_t index) {
	double value = 0;
	std::memcpy(&value, &floats[index], sizeof(value));
	return value;
}

static void fp_write_single(uint64_t *floats, uint32_t index, float value) {
	uint32_t bits = 0;
	std::memcpy(&bits, &value, sizeof(bits));
	floats[index] = UINT64_C(0xFFFFFFFF00000000) | bits;
}

static void fp_write_double(uint64_t *floats, uint32_t index, double value) {
	std::memcpy(&floats[index], &value, sizeof(value));
}

static bool fp_raw_rounding_mode(uint32_t rm, const uint64_t *fcsr, int *mode) {
	if (rm == 7) {
		if (fcsr == nullptr) {
			return false;
		}
		rm = static_cast<uint32_t>((*fcsr >> 5) & 0x7);
	}
	switch (rm) {
	case 0:
		*mode = FE_TONEAREST;
		return true;
	case 1:
		*mode = FE_TOWARDZERO;
		return true;
	case 2:
		*mode = FE_DOWNWARD;
		return true;
	case 3:
		*mode = FE_UPWARD;
		return true;
	default:
		return false;
	}
}

static uint64_t fp_classify_bits(uint64_t bits, bool single) {
	const uint32_t exponent_bits = single ? 8 : 11;
	const uint32_t fraction_bits = single ? 23 : 52;
	const uint32_t sign_shift = single ? 31 : 63;
	const uint64_t fraction_mask = (UINT64_C(1) << fraction_bits) - 1;
	const uint64_t exponent_mask = (UINT64_C(1) << exponent_bits) - 1;
	const bool negative = ((bits >> sign_shift) & 1) != 0;
	const uint64_t exponent = (bits >> fraction_bits) & exponent_mask;
	const uint64_t fraction = bits & fraction_mask;
	if (exponent == exponent_mask) {
		if (fraction == 0) {
			return UINT64_C(1) << (negative ? 0 : 7);
		}
		if ((fraction >> (fraction_bits - 1)) & 1) {
			return UINT64_C(1) << 9;
		}
		return UINT64_C(1) << 8;
	}
	if (exponent == 0) {
		if (fraction == 0) {
			return UINT64_C(1) << (negative ? 3 : 4);
		}
		return UINT64_C(1) << (negative ? 2 : 5);
	}
	return UINT64_C(1) << (negative ? 1 : 6);
}

static uint64_t fp_integer_result(bool signed_value, uint32_t bits, bool negative) {
	if (signed_value) {
		if (bits == 32) {
			return negative ? static_cast<uint64_t>(std::numeric_limits<int32_t>::min())
			                : static_cast<uint64_t>(std::numeric_limits<int32_t>::max());
		}
		return negative ? static_cast<uint64_t>(std::numeric_limits<int64_t>::min())
		                : static_cast<uint64_t>(std::numeric_limits<int64_t>::max());
	}
	if (negative) {
		return 0;
	}
	return bits == 32 ? static_cast<uint64_t>(std::numeric_limits<uint32_t>::max())
	                  : std::numeric_limits<uint64_t>::max();
}

static bool fp_convert_to_integer(double value, uint32_t rm, const uint64_t *fcsr,
                                  bool signed_value, uint32_t bits, uint64_t *result,
                                  bool *invalid) {
	int mode = FE_TONEAREST;
	if (!fp_raw_rounding_mode(rm, fcsr, &mode)) {
		return false;
	}
	*invalid = false;
	if (!std::isfinite(value)) {
		*invalid = true;
		*result = fp_integer_result(signed_value, bits, value < 0);
		return true;
	}
	double rounded = 0;
	switch (mode) {
	case FE_TONEAREST:
		rounded = std::nearbyint(value);
		break;
	case FE_TOWARDZERO:
		rounded = std::trunc(value);
		break;
	case FE_DOWNWARD:
		rounded = std::floor(value);
		break;
	default:
		rounded = std::ceil(value);
		break;
	}
	if (signed_value) {
		const double minimum = bits == 32
		                           ? static_cast<double>(std::numeric_limits<int32_t>::min())
		                           : static_cast<double>(std::numeric_limits<int64_t>::min());
		const double maximum = bits == 32
		                           ? static_cast<double>(std::numeric_limits<int32_t>::max())
		                           : static_cast<double>(std::numeric_limits<int64_t>::max());
		if (rounded < minimum || rounded > maximum) {
			*invalid = true;
			*result = fp_integer_result(true, bits, rounded < 0);
			return true;
		}
		*result = bits == 32 ? static_cast<uint64_t>(static_cast<int64_t>(static_cast<int32_t>(rounded)))
		                     : static_cast<uint64_t>(static_cast<int64_t>(rounded));
		return true;
	}
	const double maximum = bits == 32
	                           ? static_cast<double>(std::numeric_limits<uint32_t>::max())
	                           : static_cast<double>(std::numeric_limits<uint64_t>::max());
	if (rounded < 0 || rounded > maximum) {
		*invalid = true;
		*result = fp_integer_result(false, bits, rounded < 0);
		return true;
	}
	*result = bits == 32 ? static_cast<uint64_t>(static_cast<uint32_t>(rounded))
	                     : static_cast<uint64_t>(rounded);
	return true;
}

static bool fp_execute_raw(uint64_t *registers, uint64_t *floats, uint64_t *fcsr,
                           uint32_t raw) {
	if (registers == nullptr || floats == nullptr || fcsr == nullptr) {
		return false;
	}
	const uint32_t opcode = raw & 0x7F;
	const uint32_t rd = (raw >> 7) & 0x1F;
	const uint32_t funct3 = (raw >> 12) & 0x7;
	const uint32_t rs1 = (raw >> 15) & 0x1F;
	const uint32_t rs2 = (raw >> 20) & 0x1F;
	const uint32_t funct7 = raw >> 25;
	if (opcode == 0x43 || opcode == 0x47 || opcode == 0x4B || opcode == 0x4F) {
		// 融合乘加继续走 Go 单步语义，避免与 libm 的精确零/异常
		// 规则产生差异；其余 FSGNJ/FCVT/FMV 已在批量路径覆盖。
		return false;
	}
	if (opcode != 0x53) {
		return false;
	}
	const bool single = (funct7 & 1) == 0;
	switch (funct7 >> 1) {
	case 0x08: { // FSGNJ/FSGNJN/FSGNJX
		const uint32_t shift = single ? 31 : 63;
		const uint64_t mask = UINT64_C(1) << shift;
		const uint64_t left = floats[rs1];
		const uint64_t right = floats[rs2];
		uint64_t result = 0;
		switch (funct3) {
		case 0:
			result = (left & ~mask) | (right & mask);
			break;
		case 1:
			result = (left & ~mask) | (~right & mask);
			break;
		case 2:
			result = (left & ~mask) | ((left ^ right) & mask);
			break;
		default:
			return false;
		}
		if (single) {
			floats[rd] = UINT64_C(0xFFFFFFFF00000000) | static_cast<uint32_t>(result);
		} else {
			floats[rd] = result;
		}
		return true;
	}
	case 0x10: { // FCVT.S.D / FCVT.D.S
		if (rs2 != (single ? 1U : 0U) || !fp_rounding_supported(funct3, fcsr)) {
			return false;
		}
		const int previous_round = std::fegetround();
		if (previous_round != FE_TONEAREST) {
			std::fesetround(FE_TONEAREST);
		}
		std::feclearexcept(FE_ALL_EXCEPT);
		if (single) {
			fp_write_single(floats, rd, static_cast<float>(fp_read_double(floats, rs1)));
		} else {
			fp_write_double(floats, rd, static_cast<double>(fp_read_single(floats, rs1)));
		}
		*fcsr |= fp_flags_from_host(std::fetestexcept(FE_ALL_EXCEPT));
		if (previous_round != FE_TONEAREST) {
			std::fesetround(previous_round);
		}
		return true;
	}
	case 0x38: { // FMV.X.W/D 与 FCLASS.S/D
		if (rs2 == 1) {
			registers[rd] = fp_classify_bits(floats[rs1], single);
			return true;
		}
		if (rs2 != 0) {
			return false;
		}
		if (single) {
			registers[rd] = static_cast<uint64_t>(
			    static_cast<int64_t>(static_cast<int32_t>(static_cast<uint32_t>(floats[rs1]))));
		} else {
			registers[rd] = floats[rs1];
		}
		return true;
	}
	case 0x3C: { // FMV.W.X / FMV.D.X
		if (rs2 != 0) {
			return false;
		}
		floats[rd] = single
		                 ? UINT64_C(0xFFFFFFFF00000000) | static_cast<uint32_t>(registers[rs1])
		                 : registers[rs1];
		return true;
	}
	case 0x30: { // FCVT.W/WU/L/LU.S 或 .D
		if (rs2 > 3) {
			return false;
		}
		const double value = single ? static_cast<double>(fp_read_single(floats, rs1))
		                            : fp_read_double(floats, rs1);
		const bool signed_value = rs2 == 0 || rs2 == 2;
		const uint32_t bits = rs2 >= 2 ? 64 : 32;
		bool invalid = false;
		if (!fp_convert_to_integer(value, funct3, fcsr, signed_value, bits,
		                           &registers[rd], &invalid)) {
			return false;
		}
		if (invalid) {
			*fcsr |= 1;
		}
		return true;
	}
	case 0x34: { // FCVT.S/D.W/WU/L/LU
		if (rs2 > 3) {
			return false;
		}
		int mode = FE_TONEAREST;
		if (!fp_raw_rounding_mode(funct3, fcsr, &mode)) {
			return false;
		}
		const int previous_round = std::fegetround();
		if (previous_round != mode) {
			std::fesetround(mode);
		}
		std::feclearexcept(FE_ALL_EXCEPT);
		double value = 0;
		switch (rs2) {
		case 0:
			value = static_cast<double>(static_cast<int32_t>(registers[rs1]));
			break;
		case 1:
			value = static_cast<double>(static_cast<uint32_t>(registers[rs1]));
			break;
		case 2:
			value = static_cast<double>(static_cast<int64_t>(registers[rs1]));
			break;
		default:
			value = static_cast<double>(registers[rs1]);
			break;
		}
		if (single) {
			fp_write_single(floats, rd, static_cast<float>(value));
		} else {
			fp_write_double(floats, rd, value);
		}
		*fcsr |= fp_flags_from_host(std::fetestexcept(FE_ALL_EXCEPT));
		if (previous_round != mode) {
			std::fesetround(previous_round);
		}
		return true;
	}
	default:
		return false;
	}
}

static bool fp_execute_csr(uint64_t *registers, uint64_t *fcsr, uint32_t raw) {
	if (registers == nullptr || fcsr == nullptr) {
		return false;
	}
	const uint32_t csr = raw >> 20;
	const uint32_t funct3 = (raw >> 12) & 0x7;
	const uint32_t rd = (raw >> 7) & 0x1F;
	const uint32_t rs1 = (raw >> 15) & 0x1F;
	if (csr != 0x001 && csr != 0x002 && csr != 0x003) {
		return false;
	}
	const uint64_t source = funct3 < 0x5 ? registers[rs1] : rs1;
	uint64_t old = *fcsr;
	if (csr == 0x001) {
		old &= 0x1F;
	} else if (csr == 0x002) {
		old = (*fcsr >> 5) & 0x7;
	} else {
		old &= 0xFF;
	}
	if (rd != 0) {
		registers[rd] = old;
	}
	const bool writes = funct3 == 0x1 || funct3 == 0x5 ||
	                    (source != 0 && (funct3 == 0x2 || funct3 == 0x3 || funct3 == 0x6 || funct3 == 0x7));
	if (!writes) {
		return true;
	}
	uint64_t updated = *fcsr;
	if (csr == 0x001) {
		if (funct3 == 0x1 || funct3 == 0x5) {
			updated = (updated & ~UINT64_C(0x1F)) | (source & 0x1F);
		} else if (funct3 == 0x2 || funct3 == 0x6) {
			updated |= source & 0x1F;
		} else {
			updated &= ~(source & 0x1F);
		}
	} else if (csr == 0x002) {
		if (funct3 == 0x1 || funct3 == 0x5) {
			updated = (updated & ~UINT64_C(0xE0)) | ((source & 0x7) << 5);
		} else if (funct3 == 0x2 || funct3 == 0x6) {
			updated |= (source & 0x7) << 5;
		} else {
			updated &= ~((source & 0x7) << 5);
		}
	} else {
		if (funct3 == 0x1 || funct3 == 0x5) {
			updated = (updated & ~UINT64_C(0xFF)) | (source & 0xFF);
		} else if (funct3 == 0x2 || funct3 == 0x6) {
			updated |= source & 0xFF;
		} else {
			updated &= ~(source & 0xFF);
		}
	}
	*fcsr = updated;
	return true;
}

#if defined(__x86_64__)

namespace {

constexpr uint32_t jit_status_clean = 0;
constexpr uint32_t jit_status_fallback = 1;
constexpr uint32_t jit_status_budget = 2;
constexpr uint64_t jit_max_straight_run = 2048;
constexpr size_t jit_max_code_bytes = 4u << 20;
constexpr size_t jit_max_pages = 1024;
constexpr uint32_t jit_tlb_entries = 512;
constexpr uint32_t jit_inline_tlb_entries = 32;
constexpr uint32_t jit_link_entries = 256;
constexpr uint64_t jit_tlb_zero_page = 1 << 9;
constexpr uint64_t jit_tlb_write_ok = 1 << 10;
constexpr uint32_t jit_page_hotness = 32;

struct JitTLBEntry {
	uint64_t page;
	uint8_t *pointer;
	uint64_t flags;
	uint64_t padding;
};

struct JitCall {
	uint64_t *registers;
	uint64_t pc;
	uint64_t retired_base;
	MemoryView *memory;
	const uint64_t *entries;
	uint64_t threshold;
	// steps 由页函数在入口装入 r15、在出口写回；跨页 tail-jump 时
	// 目标页从同一字段继续累计，避免只返回最后一页的条数。
	uint64_t steps;
	uint64_t scratch;
	uint64_t status;
	uint64_t *reservation;
	uint64_t *floats;
	uint64_t *fcsr;
	uint64_t last_page;
	uint8_t *last_pointer;
	uint64_t last_flags;
	uint64_t second_page;
	uint8_t *second_pointer;
	uint64_t second_flags;
	uint64_t last_address;
	uint64_t inline_pages[jit_inline_tlb_entries];
	uint64_t inline_flags[jit_inline_tlb_entries];
	uint8_t *inline_pointers[jit_inline_tlb_entries];
	// 跨页目标缓存按目标 PC 直接映射。三个数组分开存放，生成的
	// 代码可以直接用 scale=8 索引；JitCall 每次 jit_run 重置。
	uint64_t link_targets[jit_link_entries];
	uint64_t link_functions[jit_link_entries];
	uint64_t link_entries[jit_link_entries];
	JitTLBEntry tlb[jit_tlb_entries];
};

static_assert(offsetof(JitCall, tlb) % 8 == 0, "JitCall layout mismatch");

enum X64Reg : uint8_t {
	x64_rax = 0,
	x64_rcx = 1,
	x64_rdx = 2,
	x64_rbx = 3,
	x64_rsp = 4,
	x64_rbp = 5,
	x64_rsi = 6,
	x64_rdi = 7,
	x64_r8 = 8,
	x64_r9 = 9,
	x64_r10 = 10,
	x64_r11 = 11,
	x64_r12 = 12,
	x64_r13 = 13,
	x64_r14 = 14,
	x64_r15 = 15,
};

enum X64Condition : uint8_t {
	x64_b = 2,
	x64_ae = 3,
	x64_e = 4,
	x64_ne = 5,
	x64_l = 12,
	x64_ge = 13,
};

struct JitLabel {
	size_t position = SIZE_MAX;
	std::vector<size_t> patches;
};

class X64Emitter {
public:
	std::vector<uint8_t> code;

	void emit8(uint8_t value) { code.push_back(value); }

	void inc_m64(int base, int32_t displacement) {
		rex(true, 0, 0, base);
		emit8(0xFF);
		modrm_mem(0, base, displacement);
	}

	void emit32(uint32_t value) {
		for (unsigned index = 0; index < 4; index++) {
			code.push_back(static_cast<uint8_t>(value >> (index * 8)));
		}
	}

	void emit64(uint64_t value) {
		for (unsigned index = 0; index < 8; index++) {
			code.push_back(static_cast<uint8_t>(value >> (index * 8)));
		}
	}

	void rex(bool wide, int reg, int index, int base) {
		const uint8_t value = static_cast<uint8_t>(
		    0x40 | (wide ? 8 : 0) | ((reg >> 3) & 1) << 2 | ((index >> 3) & 1) << 1 |
		    ((base >> 3) & 1));
		if (value != 0x40) {
			emit8(value);
		}
	}

	void modrm_reg(int reg, int rm) {
		emit8(static_cast<uint8_t>(0xC0 | ((reg & 7) << 3) | (rm & 7)));
	}

	void modrm_mem(int reg, int base, int32_t displacement) {
		emit8(static_cast<uint8_t>(0x80 | ((reg & 7) << 3) | (base & 7)));
		if ((base & 7) == 4) {
			emit8(0x24);
		}
		emit32(static_cast<uint32_t>(displacement));
	}

	void modrm_index(int reg, int base, int index, int scale) {
		emit8(static_cast<uint8_t>(0x04 | ((reg & 7) << 3)));
		emit8(static_cast<uint8_t>(((scale & 3) << 6) | ((index & 7) << 3) | (base & 7)));
	}

	void modrm_index_disp32(int reg, int base, int index, int scale) {
		emit8(static_cast<uint8_t>(0x84 | ((reg & 7) << 3)));
		emit8(static_cast<uint8_t>(((scale & 3) << 6) | ((index & 7) << 3) | (base & 7)));
	}

	void mov_rr(int destination, int source, bool wide = true) {
		rex(wide, source, 0, destination);
		emit8(0x89);
		modrm_reg(source, destination);
	}

	void mov_ri64(int destination, uint64_t value) {
		rex(true, 0, 0, destination);
		emit8(static_cast<uint8_t>(0xB8 + (destination & 7)));
		emit64(value);
	}

	void mov_ri32_sign(int destination, int32_t value) {
		rex(true, 0, 0, destination);
		emit8(0xC7);
		modrm_reg(0, destination);
		emit32(static_cast<uint32_t>(value));
	}

	void mov_mi32(int base, int32_t displacement, int32_t value) {
		rex(true, 0, 0, base);
		emit8(0xC7);
		modrm_mem(0, base, displacement);
		emit32(static_cast<uint32_t>(value));
	}

	void mov_rm(int destination, int base, int32_t displacement, bool wide = true) {
		rex(wide, destination, 0, base);
		emit8(0x8B);
		modrm_mem(destination, base, displacement);
	}

	void movzx_m8(int destination, int base, int32_t displacement) {
		rex(true, destination, 0, base);
		emit8(0x0F);
		emit8(0xB6);
		modrm_mem(destination, base, displacement);
	}

	void movzx_m16(int destination, int base, int32_t displacement) {
		rex(true, destination, 0, base);
		emit8(0x0F);
		emit8(0xB7);
		modrm_mem(destination, base, displacement);
	}

	void movsx_m8(int destination, int base, int32_t displacement) {
		rex(true, destination, 0, base);
		emit8(0x0F);
		emit8(0xBE);
		modrm_mem(destination, base, displacement);
	}

	void movsx_m16(int destination, int base, int32_t displacement) {
		rex(true, destination, 0, base);
		emit8(0x0F);
		emit8(0xBF);
		modrm_mem(destination, base, displacement);
	}

	void movsxd_m32(int destination, int base, int32_t displacement) {
		rex(true, destination, 0, base);
		emit8(0x63);
		modrm_mem(destination, base, displacement);
	}

	void mov_mr(int base, int32_t displacement, int source, bool wide = true) {
		rex(wide, source, 0, base);
		emit8(0x89);
		modrm_mem(source, base, displacement);
	}

	void mov_m8(int base, int32_t displacement, int source) {
		rex(false, source, 0, base);
		emit8(0x88);
		modrm_mem(source, base, displacement);
	}

	void mov_m16(int base, int32_t displacement, int source) {
		emit8(0x66);
		rex(false, source, 0, base);
		emit8(0x89);
		modrm_mem(source, base, displacement);
	}

	void mov_m32(int base, int32_t displacement, int source) {
		rex(false, source, 0, base);
		emit8(0x89);
		modrm_mem(source, base, displacement);
	}

	void lea(int destination, int base, int32_t displacement) {
		rex(true, destination, 0, base);
		emit8(0x8D);
		modrm_mem(destination, base, displacement);
	}

	void alu_rr(uint8_t opcode, int destination, int source, bool wide = true) {
		rex(wide, source, 0, destination);
		emit8(opcode);
		modrm_reg(source, destination);
	}

	void add_rr(int destination, int source) { alu_rr(0x01, destination, source); }
	void add_rr32(int destination, int source) { alu_rr(0x01, destination, source, false); }
	void sub_rr(int destination, int source) { alu_rr(0x29, destination, source); }
	void sub_rr32(int destination, int source) { alu_rr(0x29, destination, source, false); }
	void and_rr(int destination, int source) { alu_rr(0x21, destination, source); }
	void or_rr(int destination, int source) { alu_rr(0x09, destination, source); }
	void xor_rr(int destination, int source) { alu_rr(0x31, destination, source); }
	void cmp_rr(int destination, int source) { alu_rr(0x39, destination, source); }
	void test_rr(int destination, int source) { alu_rr(0x85, destination, source); }

	void cmp_rm(int left, int base, int32_t displacement) {
		rex(true, left, 0, base);
		emit8(0x3B);
		modrm_mem(left, base, displacement);
	}

	void test_al_al() {
		emit8(0x84);
		emit8(0xC0);
	}

	void alu_imm(uint8_t group, int destination, int32_t value, bool wide = true) {
		rex(wide, 0, 0, destination);
		if (value >= -128 && value <= 127) {
			emit8(0x83);
			modrm_reg(group, destination);
			emit8(static_cast<uint8_t>(value));
			return;
		}
		emit8(0x81);
		modrm_reg(group, destination);
		emit32(static_cast<uint32_t>(value));
	}

	void add_imm(int destination, int32_t value) { alu_imm(0, destination, value); }
	void or_imm(int destination, int32_t value) { alu_imm(1, destination, value); }
	void and_imm(int destination, int32_t value) { alu_imm(4, destination, value); }
	void sub_imm(int destination, int32_t value) { alu_imm(5, destination, value); }
	void xor_imm(int destination, int32_t value) { alu_imm(6, destination, value); }
	void cmp_imm(int destination, int32_t value) { alu_imm(7, destination, value); }

	void add_imm32(int destination, int32_t value) {
		alu_imm(0, destination, value, false);
	}

	void and_imm32(int destination, int32_t value) {
		alu_imm(4, destination, value, false);
	}

	void test_m_imm32(int base, int32_t displacement, int32_t value) {
		rex(true, 0, 0, base);
		emit8(0xF7);
		modrm_mem(0, base, displacement);
		emit32(static_cast<uint32_t>(value));
	}

	void shr_imm(int destination, uint8_t amount) {
		shift_imm(destination, 5, amount);
	}

	void shl_imm(int destination, uint8_t amount) {
		shift_imm(destination, 4, amount);
	}

	void shift_imm(int destination, uint8_t group, uint8_t amount) {
		rex(true, 0, 0, destination);
		emit8(0xC1);
		modrm_reg(group, destination);
		emit8(amount);
	}

	void shift_imm32(int destination, uint8_t group, uint8_t amount) {
		rex(false, 0, 0, destination);
		emit8(0xC1);
		modrm_reg(group, destination);
		emit8(amount);
	}

	void shift_cl(int destination, uint8_t group, bool wide = true) {
		rex(wide, 0, 0, destination);
		emit8(0xD3);
		modrm_reg(group, destination);
	}

	void imul_rr32(int destination, int source) {
		rex(false, destination, 0, source);
		emit8(0x0F);
		emit8(0xAF);
		modrm_reg(destination, source);
	}

	void imul_rr(int destination, int source) {
		rex(true, destination, 0, source);
		emit8(0x0F);
		emit8(0xAF);
		modrm_reg(destination, source);
	}

	void movsxd(int destination, int source) {
		rex(true, destination, 0, source);
		emit8(0x63);
		modrm_reg(destination, source);
	}

	void setcc_al(uint8_t condition) {
		emit8(0x0F);
		emit8(static_cast<uint8_t>(0x90 + condition));
		emit8(0xC0);
	}

	void movzx_eax_al() {
		rex(false, x64_rax, 0, x64_rax);
		emit8(0x0F);
		emit8(0xB6);
		modrm_reg(x64_rax, x64_rax);
	}

	void cmovcc(int destination, int source, uint8_t condition) {
		rex(true, destination, 0, source);
		emit8(0x0F);
		emit8(static_cast<uint8_t>(0x40 + condition));
		modrm_reg(destination, source);
	}

	void call_reg(int reg) {
		rex(false, 0, 0, reg);
		emit8(0xFF);
		modrm_reg(2, reg);
	}

	void jmp_reg(int reg) {
		rex(false, 0, 0, reg);
		emit8(0xFF);
		modrm_reg(4, reg);
	}

	void jmp_indexed(int base, int index, int scale) {
		rex(false, 0, index, base);
		emit8(0xFF);
		modrm_index(4, base, index, scale);
	}

	void mov_rm_indexed(int destination, int base, int index, int scale, int32_t displacement) {
		rex(true, destination, index, base);
		emit8(0x8B);
		modrm_index_disp32(destination, base, index, scale);
		emit32(static_cast<uint32_t>(displacement));
	}

	void cmp_rm_indexed(int left, int base, int index, int scale, int32_t displacement) {
		rex(true, left, index, base);
		emit8(0x3B);
		modrm_index_disp32(left, base, index, scale);
		emit32(static_cast<uint32_t>(displacement));
	}

	void test_mi_indexed32(int base, int index, int scale, int32_t displacement, int32_t value) {
		rex(true, 0, index, base);
		emit8(0xF7);
		modrm_index_disp32(0, base, index, scale);
		emit32(static_cast<uint32_t>(displacement));
		emit32(static_cast<uint32_t>(value));
	}

	void push(int reg) {
		rex(false, 0, 0, reg);
		emit8(static_cast<uint8_t>(0x50 + (reg & 7)));
	}

	void pop(int reg) {
		rex(false, 0, 0, reg);
		emit8(static_cast<uint8_t>(0x58 + (reg & 7)));
	}

	void ret() { emit8(0xC3); }

	void bind(JitLabel &label) {
		label.position = code.size();
		for (size_t patch : label.patches) {
			const int64_t displacement =
			    static_cast<int64_t>(label.position) - static_cast<int64_t>(patch + 4);
			const uint32_t encoded = static_cast<uint32_t>(displacement);
			for (unsigned index = 0; index < 4; index++) {
				code[patch + index] = static_cast<uint8_t>(encoded >> (index * 8));
			}
		}
		label.patches.clear();
	}

	void jmp(JitLabel &label) {
		emit8(0xE9);
		if (label.position != SIZE_MAX) {
			const int64_t displacement =
			    static_cast<int64_t>(label.position) - static_cast<int64_t>(code.size() + 4);
			emit32(static_cast<uint32_t>(displacement));
			return;
		}
		label.patches.push_back(code.size());
		emit32(0);
	}

	void jcc(JitLabel &label, uint8_t condition) {
		emit8(0x0F);
		emit8(static_cast<uint8_t>(0x80 + condition));
		if (label.position != SIZE_MAX) {
			const int64_t displacement =
			    static_cast<int64_t>(label.position) - static_cast<int64_t>(code.size() + 4);
			emit32(static_cast<uint32_t>(displacement));
			return;
		}
		label.patches.push_back(code.size());
		emit32(0);
	}
};

static uint64_t jit_mul_high(uint64_t left, uint64_t right, uint32_t kind) {
	switch (kind) {
	case 0: {
		const __int128 product =
		    static_cast<__int128>(static_cast<int64_t>(left)) * static_cast<__int128>(static_cast<int64_t>(right));
		return static_cast<uint64_t>(product >> 64);
	}
	case 1: {
		const __int128 product =
		    static_cast<__int128>(static_cast<int64_t>(left)) * static_cast<unsigned __int128>(right);
		return static_cast<uint64_t>(product >> 64);
	}
	default: {
		const unsigned __int128 product =
		    static_cast<unsigned __int128>(left) * static_cast<unsigned __int128>(right);
		return static_cast<uint64_t>(product >> 64);
	}
	}
}

static uint64_t jit_divrem(uint64_t left, uint64_t right, uint32_t kind) {
	switch (kind) {
	case 0:
		return divide_signed(left, right);
	case 1:
		return divide_unsigned(left, right);
	case 2:
		return remainder_signed(left, right);
	case 3:
		return remainder_unsigned(left, right);
	case 4:
		return static_cast<uint64_t>(sign_extend(
		    divide_signed_word(static_cast<uint32_t>(left), static_cast<uint32_t>(right)), 32));
	case 5:
		return static_cast<uint64_t>(sign_extend(
		    divide_unsigned_word(static_cast<uint32_t>(left), static_cast<uint32_t>(right)), 32));
	case 6:
		return static_cast<uint64_t>(sign_extend(
		    remainder_signed_word(static_cast<uint32_t>(left), static_cast<uint32_t>(right)), 32));
	default:
		return static_cast<uint64_t>(sign_extend(
		    remainder_unsigned_word(static_cast<uint32_t>(left), static_cast<uint32_t>(right)), 32));
	}
}

static bool jit_memory_load(MemoryView *memory, uint64_t address, uint32_t width,
                            uint32_t is_signed, uint64_t *result) {
	const uint8_t *source = memory->translate(address, width, false, 0);
	if (source == nullptr) {
		return false;
	}
	*result = load_value(source, width, is_signed != 0);
	return true;
}

static bool jit_memory_store(MemoryView *memory, uint64_t address, uint32_t width,
                             uint64_t value, uint64_t *reservation) {
	uint8_t *destination = memory->translate(address, width, true, 0);
	if (destination == nullptr) {
		return false;
	}
	__builtin_memcpy(destination, &value, width);
	invalidate_reservation(reservation, address, width);
	return true;
}

static bool jit_memory_amo(MemoryView *memory, uint64_t address, uint64_t operand,
                           uint32_t spec, uint64_t *old_result,
                           uint64_t *reservation) {
	const uint32_t width = spec & 0xFF;
	const uint32_t funct5 = spec >> 8;
	if ((width != 4 && width != 8) || address % width != 0) {
		return false;
	}
	uint8_t *destination = memory->translate(address, width, true, 0);
	if (destination == nullptr) {
		return false;
	}
	uint64_t old_value = 0;
	std::memcpy(&old_value, destination, width);
	const uint64_t updated = apply_amo(old_value, operand, funct5, width == 4);
	std::memcpy(destination, &updated, width);
	invalidate_reservation(reservation, address, width);
	if (width == 4) {
		*old_result = static_cast<uint64_t>(
		    static_cast<int64_t>(static_cast<int32_t>(static_cast<uint32_t>(old_value))));
	} else {
		*old_result = old_value;
	}
	return true;
}

static uint8_t *jit_tlb_resolve(JitCall *call, uint64_t address, uint32_t width,
                                uint32_t write_access) {
	if (call == nullptr || call->memory == nullptr) {
		return nullptr;
	}
	if ((address & 4095) + width > 4096) {
		return nullptr;
	}
	const uint64_t page = address >> 12;
	const uint32_t slot = static_cast<uint32_t>((page ^ (page >> 7)) & (jit_tlb_entries - 1));
	JitTLBEntry &entry = call->tlb[slot];
	uint64_t flags = entry.flags;
	uint8_t *pointer = entry.pointer;
	if (entry.page != page || entry.pointer == nullptr) {
		const uint64_t page_address = page << 12;
		flags = 0;
		pointer = call->memory->translate(page_address, 1, write_access != 0, 0, &flags);
		if (pointer == nullptr) {
			return nullptr;
		}
		if (!write_access && call->memory->find_page(page) == nullptr) {
			flags |= jit_tlb_zero_page;
		}
		entry.page = page;
		entry.pointer = pointer;
		entry.flags = flags;
	}
	if (write_access &&
	    ((flags & prot_write) == 0 || (flags & jit_tlb_zero_page) != 0 ||
	     (flags & prot_exec) != 0)) {
		return nullptr;
	}
	if ((flags & prot_write) != 0 && (flags & jit_tlb_zero_page) == 0 &&
	    (flags & prot_exec) == 0) {
		flags |= jit_tlb_write_ok;
	}
	call->second_page = call->last_page;
	call->second_pointer = call->last_pointer;
	call->second_flags = call->last_flags;
	call->last_page = page;
	call->last_pointer = pointer;
	call->last_flags = flags;
	const uint32_t inline_slot = slot & (jit_inline_tlb_entries - 1);
	call->inline_pages[inline_slot] = page;
	call->inline_pointers[inline_slot] = pointer;
	call->inline_flags[inline_slot] = flags;
	return pointer;
}

static bool jit_memory_lr(MemoryView *memory, uint64_t address, uint32_t width,
                          uint64_t *reservation, uint64_t *result) {
	if ((width != 4 && width != 8) || address % width != 0) {
		return false;
	}
	const uint8_t *source = memory->translate(address, width, false, 0);
	if (source == nullptr) {
		return false;
	}
	const uint64_t value = load_value(source, width, false);
	*result = width == 4
	              ? static_cast<uint64_t>(static_cast<int64_t>(
	                    static_cast<int32_t>(static_cast<uint32_t>(value))))
	              : value;
	reservation[0] = address;
	reservation[1] = width;
	reservation[2] = value;
	reservation[3] = 1;
	return true;
}

static bool jit_memory_sc(MemoryView *memory, uint64_t address, uint32_t width,
                          uint64_t value, uint64_t *reservation, uint64_t *result) {
	if (reservation == nullptr || reservation[3] == 0 || reservation[0] != address ||
	    reservation[1] != width) {
		*result = 1;
		return true;
	}
	uint8_t *destination = memory->translate(address, width, true, 0);
	if (destination == nullptr) {
		return false;
	}
	uint64_t old_value = 0;
	std::memcpy(&old_value, destination, width);
	reservation[3] = 0;
	if (old_value != reservation[2]) {
		*result = 1;
		return true;
	}
	std::memcpy(destination, &value, width);
	*result = 0;
	return true;
}

static bool jit_execute_fp(JitCall *call, uint64_t encoded) {
	if (call == nullptr) {
		return false;
	}
	return fp_execute(call->registers, call->floats, call->fcsr, encoded,
	                  &call->scratch);
}

static bool jit_execute_fp_raw(JitCall *call, uint64_t raw) {
	if (call == nullptr) {
		return false;
	}
	return fp_execute_raw(call->registers, call->floats, call->fcsr,
	                      static_cast<uint32_t>(raw));
}

static bool jit_execute_fp_csr(JitCall *call, uint64_t raw) {
	if (call == nullptr) {
		return false;
	}
	return fp_execute_csr(call->registers, call->fcsr, static_cast<uint32_t>(raw));
}

static uint64_t jit_mix(uint64_t value) {
	value ^= value >> 30;
	value *= UINT64_C(0xBF58476D1CE4E5B9);
	value ^= value >> 27;
	value *= UINT64_C(0x94D049BB133111EB);
	return value ^ (value >> 31);
}

struct JitPageKey {
	uint64_t identity;
	uint64_t version;
	uint64_t base;
	uint32_t start_slot;

	bool operator==(const JitPageKey &other) const {
		return identity == other.identity && version == other.version &&
		       base == other.base && start_slot == other.start_slot;
	}
};

struct JitPageKeyHash {
	size_t operator()(const JitPageKey &key) const {
		uint64_t hash = jit_mix(key.identity);
		hash ^= jit_mix(key.version + UINT64_C(0x9E3779B97F4A7C15));
		hash ^= jit_mix(key.base);
		hash ^= jit_mix(key.start_slot + UINT64_C(0x9E3779B97F4A7C15));
		return static_cast<size_t>(hash);
	}
};

using JitPageFunction = uint64_t (*)(JitCall *);

struct JitPage {
	std::vector<uint8_t> code;
	JitPageFunction function = nullptr;
	const uint64_t *entries = nullptr;
	void *mapping = nullptr;
	size_t mapping_size = 0;
};

static uint32_t jit_link_slot(uint64_t pc) {
	return static_cast<uint32_t>((pc ^ (pc >> 7)) & (jit_link_entries - 1));
}

static void jit_link_fill(JitCall *call, uint64_t pc, JitPage *page) {
	if (call == nullptr || page == nullptr || page->function == nullptr || page->entries == nullptr) {
		return;
	}
	const uint32_t slot = jit_link_slot(pc);
	call->link_targets[slot] = pc;
	call->link_functions[slot] = reinterpret_cast<uint64_t>(page->function);
	call->link_entries[slot] = reinterpret_cast<uint64_t>(page->entries);
}

struct JitPageHint {
	JitPageKey key{};
	JitPage *page = nullptr;
	bool valid = false;
};

class JitCodeCache {
public:
	std::mutex mutex;
	std::unordered_map<JitPageKey, JitPage *, JitPageKeyHash> pages;
	std::unordered_map<JitPageKey, uint32_t, JitPageKeyHash> warmup;

	JitPage *lookup(const JitPageKey &key, bool &compile) {
		thread_local JitPageHint hints[256];
		JitPageHint &hint = hints[(key.base >> 12) & 255];
		if (hint.valid && hint.key == key) {
			return hint.page;
		}
		std::lock_guard<std::mutex> guard(mutex);
		const auto found = pages.find(key);
		if (found == pages.end()) {
			if (pages.size() < jit_max_pages) {
				if (warmup.size() >= 8192) {
					warmup.clear();
				}
				compile = ++warmup[key] >= jit_page_hotness;
			}
			return nullptr;
		}
		hint.key = key;
		hint.page = found->second;
		hint.valid = true;
		return found->second;
	}

	JitPage *insert(const JitPageKey &key, JitPage *page) {
		std::lock_guard<std::mutex> guard(mutex);
		if (pages.size() >= jit_max_pages) {
			return nullptr;
		}
		const auto [position, inserted] = pages.emplace(key, page);
		return inserted ? page : position->second;
	}

};

static JitCodeCache &jit_cache() {
	static JitCodeCache cache;
	return cache;
}

static bool jit_disabled() {
	static const bool disabled = std::getenv("ASSISTANT_SANDBOX_DISABLE_JIT") != nullptr;
	return disabled;
}

static bool jit_links_disabled() {
	static const bool disabled = std::getenv("ASSISTANT_SANDBOX_DISABLE_JIT_LINK") != nullptr;
	return disabled;
}

class JitCompiler {
public:
	JitCompiler(uint64_t base_value, uint32_t slots_value,
	            const DecodedInstruction *decoded_value, uint32_t start_slot_value)
	    : base(base_value), slots(slots_value), start_slot(start_slot_value),
	      decoded(decoded_value),
	      starts(slots_value, false), primary(slots_value, false),
	      labels(slots_value), host_map{} {
		for (int &value : host_map) {
			value = -1;
		}
	}

	JitPage *compile();

private:
	uint64_t base;
	uint32_t slots;
	uint32_t start_slot;
	const DecodedInstruction *decoded;
	std::vector<bool> starts;
	std::vector<bool> primary;
	std::vector<JitLabel> labels;
	int host_map[32];
	bool dirty[32] = {};
	X64Emitter emitter;
	struct FailureStub {
		JitLabel label;
		uint64_t pc;
	};
	std::vector<FailureStub> failure_stubs;
	std::unordered_map<uint64_t, size_t> failure_index;
	JitLabel entry_fallback;
	JitLabel exit_clean;
	JitLabel exit_fallback;
	JitLabel exit_budget;
	JitLabel link_stub;
	JitLabel link_miss;
	JitLabel epilogue;
	uint32_t mapped_count = 0;
	bool compiling_primary = true;

	void scanInstructions();
	void chooseRegisters();
	void emitPrologue();
	void emitInstruction(uint32_t slot, const DecodedInstruction &instruction);
	void emitEpilogue();
	void emitEpilogueBody(bool with_return);
	void emitLinkStub();
	void emitExit(JitLabel &target);
	void emitNextPC(uint32_t slot);
	void emitBranch(JitLabel &taken, int left, int right, uint32_t condition,
	                uint64_t target_pc, uint64_t fallthrough_pc, bool target_local,
	                bool fallthrough_local);
	void emitJumpToPC(uint64_t pc, bool local, bool fallback);
	void emitBudgetCheckThenJump(uint64_t pc, bool local, bool fallback);
	void emitMemoryFail(uint64_t pc);
	JitLabel &addFailureStub(uint64_t pc);
	void emitTLBLookup(uint8_t rs1, int32_t immediate, uint32_t width, bool write_access,
	                   uint64_t pc, JitLabel &access);
	void loadInto(int destination, uint8_t guest);
	int operandReg(uint8_t guest, int scratch);
	void storeRegister(uint8_t guest, int source);
	void reloadRegister(uint8_t guest);
	void emitCallHelper(uint64_t function);
	void emitStep();
	void emitConst(uint64_t value);

	bool isLocal(uint64_t pc) const {
		if (pc < base || pc >= base + static_cast<uint64_t>(slots) * 2) {
			return false;
		}
		const uint64_t offset = pc - base;
		if ((offset & 1) != 0) {
			return false;
		}
		const uint32_t slot = static_cast<uint32_t>(offset >> 1);
		return slot < slots && starts[slot];
	}

};

void JitCompiler::scanInstructions() {
	for (uint32_t slot = start_slot; slot < slots;) {
		const DecodedInstruction &instruction = decoded[slot];
		if (instruction.op == op_unsupported ||
		    (instruction.length != 2 && instruction.length != 4)) {
			slot++;
			continue;
		}
		starts[slot] = true;
		primary[slot] = true;
		slot += instruction.length / 2;
	}
}

void JitCompiler::chooseRegisters() {
	uint64_t uses[32] = {};
	uint32_t instructions = 0;
	for (uint32_t slot = 0; slot < slots; slot++) {
		if (!starts[slot]) {
			continue;
		}
		const DecodedInstruction &instruction = decoded[slot];
		instructions++;
		if (instruction.rs1 != 0) {
			uses[instruction.rs1]++;
		}
		if (instruction.rs2 != 0) {
			uses[instruction.rs2]++;
		}
		if (instruction.rd != 0) {
			uses[instruction.rd]++;
		}
	}
	if (instructions < 2) {
		return;
	}
	static constexpr int host_registers[] = {x64_rbx, x64_r12, x64_r13};
	for (uint32_t rank = 0; rank < 3; rank++) {
		uint8_t best = 0;
		for (uint8_t index = 1; index < 32; index++) {
			if (host_map[index] >= 0 || uses[index] < 2) {
				continue;
			}
			if (best == 0 || uses[index] > uses[best]) {
				best = index;
			}
		}
		if (best == 0) {
			break;
		}
		host_map[best] = host_registers[rank];
		mapped_count++;
	}
}

void JitCompiler::emitConst(uint64_t value) {
	emitter.mov_ri64(x64_rax, value);
}

void JitCompiler::loadInto(int destination, uint8_t guest) {
	if (guest == 0) {
		emitter.xor_rr(destination, destination);
		return;
	}
	if (host_map[guest] >= 0) {
		if (host_map[guest] != destination) {
			emitter.mov_rr(destination, host_map[guest]);
		}
		return;
	}
	emitter.mov_rm(destination, x64_r14, static_cast<int32_t>(guest) * 8);
}

int JitCompiler::operandReg(uint8_t guest, int scratch) {
	if (guest == 0) {
		emitter.xor_rr(scratch, scratch);
		return scratch;
	}
	if (host_map[guest] >= 0) {
		return host_map[guest];
	}
	loadInto(scratch, guest);
	return scratch;
}

void JitCompiler::storeRegister(uint8_t guest, int source) {
	if (guest == 0) {
		return;
	}
	if (host_map[guest] >= 0) {
		if (host_map[guest] != source) {
			emitter.mov_rr(host_map[guest], source);
		}
		dirty[guest] = true;
		return;
	}
	emitter.mov_mr(x64_r14, static_cast<int32_t>(guest) * 8, source);
}

void JitCompiler::reloadRegister(uint8_t guest) {
	if (guest == 0 || host_map[guest] < 0) {
		return;
	}
	emitter.mov_rm(host_map[guest], x64_r14, static_cast<int32_t>(guest) * 8);
}

void JitCompiler::emitStep() {
	emitter.rex(true, 0, 0, x64_r15);
	emitter.emit8(0xFF);
	emitter.modrm_reg(0, x64_r15);
}

void JitCompiler::emitCallHelper(uint64_t function) {
	emitter.mov_ri64(x64_rax, function);
	emitter.call_reg(x64_rax);
}

void JitCompiler::emitMemoryFail(uint64_t pc) {
	emitter.test_al_al();
	emitter.jcc(addFailureStub(pc), x64_e);
}

JitLabel &JitCompiler::addFailureStub(uint64_t pc) {
	const auto [position, inserted] =
	    failure_index.emplace(pc, failure_stubs.size());
	if (inserted) {
		failure_stubs.push_back(FailureStub{JitLabel{}, pc});
	}
	return failure_stubs[position->second].label;
}

void JitCompiler::emitTLBLookup(uint8_t rs1, int32_t immediate, uint32_t width,
                                bool write_access, uint64_t pc, JitLabel &access) {
	loadInto(x64_rax, rs1);
	if (immediate != 0) {
		emitter.add_imm(x64_rax, immediate);
	}
	emitter.mov_rr(x64_rdx, x64_rax);
	emitter.shr_imm(x64_rdx, 12);
	emitter.mov_rr(x64_r10, x64_rax);
	emitter.and_imm32(x64_r10, 4095);
	emitter.cmp_imm(x64_r10, static_cast<int32_t>(4096 - width));
	JitLabel slow;
	emitter.jcc(slow, 7); // ja
	JitLabel check_second;
	JitLabel check_inline;
	emitter.cmp_rm(x64_rdx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, last_page)));
	emitter.jcc(check_second, x64_ne);
	emitter.mov_rm(x64_r11, x64_rbp, static_cast<int32_t>(offsetof(JitCall, last_pointer)));
	if (write_access) {
		emitter.test_m_imm32(x64_rbp, static_cast<int32_t>(offsetof(JitCall, last_flags)),
		                     static_cast<int32_t>(jit_tlb_write_ok));
		emitter.jcc(addFailureStub(pc), x64_e);
	}
	emitter.jmp(access);
	emitter.bind(check_second);
	emitter.cmp_rm(x64_rdx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, second_page)));
	emitter.jcc(check_inline, x64_ne);
	emitter.mov_rm(x64_r11, x64_rbp, static_cast<int32_t>(offsetof(JitCall, second_pointer)));
	if (write_access) {
		emitter.test_m_imm32(x64_rbp, static_cast<int32_t>(offsetof(JitCall, second_flags)),
		                     static_cast<int32_t>(jit_tlb_write_ok));
		emitter.jcc(addFailureStub(pc), x64_e);
	}
	emitter.jmp(access);
	emitter.bind(check_inline);
	emitter.mov_rr(x64_rcx, x64_rdx);
	emitter.shr_imm(x64_rcx, 7);
	emitter.xor_rr(x64_rcx, x64_rdx);
	emitter.and_imm(x64_rcx, jit_inline_tlb_entries - 1);
	emitter.cmp_rm_indexed(x64_rdx, x64_rbp, x64_rcx, 3,
	                       static_cast<int32_t>(offsetof(JitCall, inline_pages)));
	emitter.jcc(slow, x64_ne);
	if (write_access) {
		JitLabel &failure = addFailureStub(pc);
		emitter.test_mi_indexed32(
		    x64_rbp, x64_rcx, 3,
		    static_cast<int32_t>(offsetof(JitCall, inline_flags)),
		    static_cast<int32_t>(jit_tlb_write_ok));
		emitter.jcc(failure, x64_e);
	}
	emitter.mov_rm_indexed(x64_r11, x64_rbp, x64_rcx, 3,
	                       static_cast<int32_t>(offsetof(JitCall, inline_pointers)));
	emitter.jmp(access);
	emitter.bind(slow);
	emitter.mov_rr(x64_rsi, x64_rax);
	emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, last_address)), x64_rax);
	emitter.mov_rr(x64_rdi, x64_rbp);
	emitter.mov_ri32_sign(x64_rdx, static_cast<int32_t>(width));
	emitter.mov_ri32_sign(x64_rcx, write_access ? 1 : 0);
	emitCallHelper(reinterpret_cast<uint64_t>(&jit_tlb_resolve));
	emitter.test_rr(x64_rax, x64_rax);
	emitter.jcc(addFailureStub(pc), x64_e);
	emitter.mov_rr(x64_r11, x64_rax);
	emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, last_address)));
	emitter.mov_rr(x64_r10, x64_rax);
	emitter.and_imm32(x64_r10, 4095);
	emitter.jmp(access);
}

void JitCompiler::emitExit(JitLabel &target) {
	emitter.jmp(target);
}

void JitCompiler::emitJumpToPC(uint64_t pc, bool local, bool fallback) {
	if (local) {
		const uint32_t slot = static_cast<uint32_t>((pc - base) >> 1);
		emitter.jmp(labels[slot]);
		return;
	}
	emitConst(pc);
	if (fallback) {
		emitter.jmp(exit_fallback);
		return;
	}
	emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)), x64_rax);
	emitter.jmp(link_stub);
}

void JitCompiler::emitBudgetCheckThenJump(uint64_t pc, bool local, bool fallback) {
	emitter.cmp_rm(x64_r15, x64_rbp, static_cast<int32_t>(offsetof(JitCall, threshold)));
	JitLabel within_budget;
	emitter.jcc(within_budget, x64_b);
	emitConst(pc);
	emitter.jmp(exit_budget);
	emitter.bind(within_budget);
	emitJumpToPC(pc, local, fallback);
}

void JitCompiler::emitNextPC(uint32_t slot) {
	const DecodedInstruction &instruction = decoded[slot];
	const uint32_t next_slot = slot + instruction.length / 2;
	const uint64_t pc = base + static_cast<uint64_t>(next_slot) * 2;
	if (next_slot >= slots) {
		emitBudgetCheckThenJump(pc, false, false);
		return;
	}
	if (starts[next_slot]) {
		if (compiling_primary && primary[next_slot]) {
			return;
		}
		emitter.jmp(labels[next_slot]);
		return;
	}
	emitConst(pc);
	emitter.jmp(exit_fallback);
}

void JitCompiler::emitBranch(JitLabel &taken, int left, int right, uint32_t condition,
                             uint64_t target_pc, uint64_t fallthrough_pc, bool target_local,
                             bool fallthrough_local) {
	emitter.cmp_rr(left, right);
	emitter.jcc(taken, static_cast<uint8_t>(condition));
	// 未跳转路径。
	emitStep();
	emitBudgetCheckThenJump(
	    fallthrough_pc, fallthrough_local,
	    !fallthrough_local && fallthrough_pc >= base &&
	        fallthrough_pc < base + static_cast<uint64_t>(slots) * 2);
	emitter.bind(taken);
	emitStep();
	emitBudgetCheckThenJump(
	    target_pc, target_local,
	    !target_local && target_pc >= base &&
	        target_pc < base + static_cast<uint64_t>(slots) * 2);
}

void JitCompiler::emitInstruction(uint32_t slot, const DecodedInstruction &instruction) {
	const uint64_t pc = base + static_cast<uint64_t>(slot) * 2;
	const uint64_t next_pc = pc + instruction.length;
	const int64_t immediate = static_cast<int64_t>(instruction.imm);
	const uint8_t rd = instruction.rd;
	const uint8_t rs1 = instruction.rs1;
	const uint8_t rs2 = instruction.rs2;

	switch (instruction.op) {
	case op_lui:
		emitConst(static_cast<uint64_t>(immediate));
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	case op_auipc:
		emitConst(pc + static_cast<uint64_t>(immediate));
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	case op_fence:
		// 单 hart 用户态下 fence 不改变可观察状态；宿主 x86-64
		// 的强内存序已覆盖普通访存顺序。fence.i 的代码页失效由
		// 地址空间版本和取指缓存处理。
		emitStep();
		emitNextPC(slot);
		return;
	case op_rdtime:
		emitter.mov_rm(x64_r10, x64_rbp,
		               static_cast<int32_t>(offsetof(JitCall, retired_base)));
		emitter.add_rr(x64_r10, x64_r15);
		storeRegister(rd, x64_r10);
		emitStep();
		emitNextPC(slot);
		return;
	case op_jal: {
		emitConst(next_pc);
		storeRegister(rd, x64_rax);
		emitStep();
		const uint64_t target = pc + static_cast<uint64_t>(immediate);
		const bool local = isLocal(target);
		emitBudgetCheckThenJump(
		    target, local,
		    !local && target >= base &&
		        target < base + static_cast<uint64_t>(slots) * 2);
		return;
	}
	case op_jalr: {
		loadInto(x64_rax, rs1);
		emitter.add_imm(x64_rax, static_cast<int32_t>(immediate));
		emitter.and_imm(x64_rax, -2);
		emitter.mov_rr(x64_r10, x64_rax);
		emitConst(next_pc);
		storeRegister(rd, x64_rax);
		emitStep();
		emitter.cmp_rm(x64_r15, x64_rbp, static_cast<int32_t>(offsetof(JitCall, threshold)));
		JitLabel budget;
		emitter.jcc(budget, x64_ae);
		emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)), x64_r10);
		emitter.mov_rr(x64_rdx, x64_r10);
		emitter.mov_ri64(x64_rsi, base);
		emitter.sub_rr(x64_rdx, x64_rsi);
		emitter.cmp_imm(x64_rdx, static_cast<int32_t>(slots * 2 - 1));
		JitLabel external;
		emitter.jcc(external, 7); // ja
		emitter.shift_imm(x64_rdx, 5, 1);
		emitter.mov_rm(x64_rcx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, entries)));
		emitter.jmp_indexed(x64_rcx, x64_rdx, 3);
		emitter.bind(external);
		emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)), x64_r10);
		emitter.jmp(link_stub);
		emitter.bind(budget);
		emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)), x64_r10);
		emitter.mov_rr(x64_rax, x64_r10);
		emitter.jmp(exit_budget);
		return;
	}
	case op_beq:
	case op_bne:
	case op_blt:
	case op_bge:
	case op_bltu:
	case op_bgeu: {
		const int left = operandReg(rs1, x64_rax);
		const int right = operandReg(rs2, x64_rcx);
		uint8_t condition = x64_e;
		switch (instruction.op) {
		case op_beq:
			condition = x64_e;
			break;
		case op_bne:
			condition = x64_ne;
			break;
		case op_blt:
			condition = x64_l;
			break;
		case op_bge:
			condition = x64_ge;
			break;
		case op_bltu:
			condition = x64_b;
			break;
		default:
			condition = x64_ae;
			break;
		}
		const uint64_t target = pc + static_cast<uint64_t>(immediate);
		JitLabel taken;
		emitBranch(taken, left, right, condition, target, next_pc,
		           isLocal(target), isLocal(next_pc));
		return;
	}
	case op_lb:
	case op_lh:
	case op_lw:
	case op_ld:
	case op_lbu:
	case op_lhu:
	case op_lwu: {
		uint32_t width = 8;
		uint32_t is_signed = 0;
		switch (instruction.op) {
		case op_lb:
			width = 1;
			is_signed = 1;
			break;
		case op_lh:
			width = 2;
			is_signed = 1;
			break;
		case op_lw:
			width = 4;
			is_signed = 1;
			break;
		case op_lbu:
			width = 1;
			break;
		case op_lhu:
			width = 2;
			break;
		case op_lwu:
			width = 4;
			break;
		default:
			break;
		}
		JitLabel access;
		emitTLBLookup(rs1, static_cast<int32_t>(immediate), width, false, pc, access);
		emitter.bind(access);
		emitter.add_rr(x64_r11, x64_r10);
		switch (width) {
		case 1:
			if (is_signed != 0) {
				emitter.movsx_m8(x64_rax, x64_r11, 0);
			} else {
				emitter.movzx_m8(x64_rax, x64_r11, 0);
			}
			break;
		case 2:
			if (is_signed != 0) {
				emitter.movsx_m16(x64_rax, x64_r11, 0);
			} else {
				emitter.movzx_m16(x64_rax, x64_r11, 0);
			}
			break;
		case 4:
			if (is_signed != 0) {
				emitter.movsxd_m32(x64_rax, x64_r11, 0);
			} else {
				emitter.mov_rm(x64_rax, x64_r11, 0, false);
			}
			break;
		default:
			emitter.mov_rm(x64_rax, x64_r11, 0);
			break;
		}
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_sb:
	case op_sh:
	case op_sw:
	case op_sd: {
		uint32_t width = 8;
		switch (instruction.op) {
		case op_sb:
			width = 1;
			break;
		case op_sh:
			width = 2;
			break;
		case op_sw:
			width = 4;
			break;
		default:
			break;
		}
		emitter.mov_rm(x64_r9, x64_rbp,
		               static_cast<int32_t>(offsetof(JitCall, reservation)));
		emitter.test_m_imm32(x64_r9, 24, -1);
		emitter.jcc(addFailureStub(pc), x64_ne);
		JitLabel access;
		emitTLBLookup(rs1, static_cast<int32_t>(immediate), width, true, pc, access);
		emitter.bind(access);
		emitter.add_rr(x64_r11, x64_r10);
		loadInto(x64_r10, rs2);
		switch (width) {
		case 1:
			emitter.mov_m8(x64_r11, 0, x64_r10);
			break;
		case 2:
			emitter.mov_m16(x64_r11, 0, x64_r10);
			break;
		case 4:
			emitter.mov_m32(x64_r11, 0, x64_r10);
			break;
		default:
			emitter.mov_mr(x64_r11, 0, x64_r10);
			break;
		}
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_addi:
	case op_slti:
	case op_sltiu:
	case op_xori:
	case op_ori:
	case op_andi:
	case op_slli:
	case op_srli:
	case op_srai: {
		int destination = x64_rax;
		if (rd != 0 && host_map[rd] >= 0 &&
		    instruction.op != op_slti && instruction.op != op_sltiu) {
			destination = host_map[rd];
			const int source = operandReg(rs1, x64_rax);
			if (source != destination) {
				emitter.mov_rr(destination, source);
			}
			dirty[rd] = true;
		} else {
			loadInto(destination, rs1);
		}
		switch (instruction.op) {
		case op_addi:
			emitter.add_imm(destination, static_cast<int32_t>(immediate));
			break;
		case op_slti:
			emitter.cmp_imm(destination, static_cast<int32_t>(immediate));
			emitter.setcc_al(x64_l);
			emitter.movzx_eax_al();
			break;
		case op_sltiu:
			emitter.cmp_imm(destination, static_cast<int32_t>(immediate));
			emitter.setcc_al(x64_b);
			emitter.movzx_eax_al();
			break;
		case op_xori:
			emitter.xor_imm(destination, static_cast<int32_t>(immediate));
			break;
		case op_ori:
			emitter.or_imm(destination, static_cast<int32_t>(immediate));
			break;
		case op_andi:
			emitter.and_imm(destination, static_cast<int32_t>(immediate));
			break;
		case op_slli:
			emitter.shift_imm(destination, 4, static_cast<uint8_t>(immediate));
			break;
		case op_srli:
			emitter.shift_imm(destination, 5, static_cast<uint8_t>(immediate));
			break;
		default:
			emitter.shift_imm(destination, 7, static_cast<uint8_t>(immediate));
			break;
		}
		storeRegister(rd, destination);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_addiw:
	case op_slliw:
	case op_srliw:
	case op_sraiw: {
		loadInto(x64_rax, rs1);
		switch (instruction.op) {
		case op_addiw:
			emitter.add_imm32(x64_rax, static_cast<int32_t>(immediate));
			break;
		case op_slliw:
			emitter.shift_imm32(x64_rax, 4, static_cast<uint8_t>(immediate));
			break;
		case op_srliw:
			emitter.shift_imm32(x64_rax, 5, static_cast<uint8_t>(immediate));
			break;
		default:
			emitter.shift_imm32(x64_rax, 7, static_cast<uint8_t>(immediate));
			break;
		}
		emitter.movsxd(x64_rax, x64_rax);
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_add:
	case op_sub:
	case op_sll:
	case op_slt:
	case op_sltu:
	case op_xor:
	case op_srl:
	case op_sra:
	case op_or:
	case op_and:
	case op_mul: {
		const bool in_place = rd != 0 && rd == rs1 && host_map[rd] >= 0 &&
		                      instruction.op != op_sll && instruction.op != op_slt &&
		                      instruction.op != op_sltu && instruction.op != op_srl &&
		                      instruction.op != op_sra;
		int destination = x64_rax;
		int left = 0;
		if (in_place) {
			destination = host_map[rd];
			left = destination;
			dirty[rd] = true;
		} else {
			left = operandReg(rs1, x64_rax);
			if (left != x64_rax) {
				emitter.mov_rr(x64_rax, left);
			}
		}
		int right = 0;
		if (instruction.op == op_sll || instruction.op == op_srl ||
		    instruction.op == op_sra) {
			loadInto(x64_rcx, rs2);
			right = x64_rcx;
		} else {
			right = operandReg(rs2, x64_rcx);
		}
		switch (instruction.op) {
		case op_add:
			emitter.add_rr(destination, right);
			break;
		case op_sub:
			emitter.sub_rr(destination, right);
			break;
		case op_sll:
			emitter.shift_cl(destination, 4);
			break;
		case op_slt:
			emitter.cmp_rr(x64_rax, right);
			emitter.setcc_al(x64_l);
			emitter.movzx_eax_al();
			break;
		case op_sltu:
			emitter.cmp_rr(x64_rax, right);
			emitter.setcc_al(x64_b);
			emitter.movzx_eax_al();
			break;
		case op_xor:
			emitter.xor_rr(destination, right);
			break;
		case op_srl:
			emitter.shift_cl(destination, 5);
			break;
		case op_sra:
			emitter.shift_cl(destination, 7);
			break;
		case op_or:
			emitter.or_rr(destination, right);
			break;
		case op_and:
			emitter.and_rr(destination, right);
			break;
		default:
			emitter.imul_rr(destination, right);
			break;
		}
		storeRegister(rd, destination);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_addw:
	case op_subw:
	case op_sllw:
	case op_srlw:
	case op_sraw:
	case op_mulw: {
		loadInto(x64_rax, rs1);
		loadInto(x64_rcx, rs2);
		switch (instruction.op) {
		case op_addw:
			emitter.add_rr32(x64_rax, x64_rcx);
			break;
		case op_subw:
			emitter.sub_rr32(x64_rax, x64_rcx);
			break;
		case op_sllw:
			emitter.shift_cl(x64_rax, 4, false);
			break;
		case op_srlw:
			emitter.shift_cl(x64_rax, 5, false);
			break;
		case op_sraw:
			emitter.shift_cl(x64_rax, 7, false);
			break;
		default:
			emitter.imul_rr32(x64_rax, x64_rcx);
			break;
		}
		emitter.movsxd(x64_rax, x64_rax);
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_mulh:
	case op_mulhsu:
	case op_mulhu:
	case op_div:
	case op_divu:
	case op_rem:
	case op_remu:
	case op_divw:
	case op_divuw:
	case op_remw:
	case op_remuw: {
		loadInto(x64_rdi, rs1);
		loadInto(x64_rsi, rs2);
		uint32_t kind = 0;
		switch (instruction.op) {
		case op_mulh:
			kind = 0;
			break;
		case op_mulhsu:
			kind = 1;
			break;
		case op_mulhu:
			kind = 2;
			break;
		case op_div:
			kind = 0;
			break;
		case op_divu:
			kind = 1;
			break;
		case op_rem:
			kind = 2;
			break;
		case op_remu:
			kind = 3;
			break;
		case op_divw:
			kind = 4;
			break;
		case op_divuw:
			kind = 5;
			break;
		case op_remw:
			kind = 6;
			break;
		default:
			kind = 7;
			break;
		}
		emitter.mov_ri32_sign(x64_rdx, static_cast<int32_t>(kind));
		if (instruction.op == op_mulh || instruction.op == op_mulhsu ||
		    instruction.op == op_mulhu) {
			emitCallHelper(reinterpret_cast<uint64_t>(&jit_mul_high));
		} else {
			emitCallHelper(reinterpret_cast<uint64_t>(&jit_divrem));
		}
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_amo: {
		loadInto(x64_rax, rs1);
		emitter.mov_rr(x64_rsi, x64_rax);
		loadInto(x64_rdx, rs2);
		emitter.mov_rm(x64_rdi, x64_rbp, static_cast<int32_t>(offsetof(JitCall, memory)));
		emitter.mov_ri32_sign(x64_rcx, instruction.imm);
		emitter.lea(x64_r8, x64_rbp, static_cast<int32_t>(offsetof(JitCall, scratch)));
		emitter.mov_rm(x64_r9, x64_rbp,
		               static_cast<int32_t>(offsetof(JitCall, reservation)));
		emitCallHelper(reinterpret_cast<uint64_t>(&jit_memory_amo));
		emitMemoryFail(pc);
		emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, scratch)));
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_lrsc: {
		const uint32_t width = static_cast<uint32_t>(instruction.imm) & 0xFF;
		const bool store_conditional =
		    ((static_cast<uint32_t>(instruction.imm) >> 8) & 0x1F) == 0x3;
		loadInto(x64_rax, rs1);
		emitter.mov_rr(x64_rsi, x64_rax);
		emitter.mov_rm(x64_rdi, x64_rbp, static_cast<int32_t>(offsetof(JitCall, memory)));
		emitter.mov_ri32_sign(x64_rdx, static_cast<int32_t>(width));
		if (store_conditional) {
			loadInto(x64_rcx, rs2);
			emitter.mov_rm(x64_r8, x64_rbp,
			               static_cast<int32_t>(offsetof(JitCall, reservation)));
			emitter.lea(x64_r9, x64_rbp, static_cast<int32_t>(offsetof(JitCall, scratch)));
			emitCallHelper(reinterpret_cast<uint64_t>(&jit_memory_sc));
		} else {
			emitter.mov_rm(x64_rcx, x64_rbp,
			               static_cast<int32_t>(offsetof(JitCall, reservation)));
			emitter.lea(x64_r8, x64_rbp, static_cast<int32_t>(offsetof(JitCall, scratch)));
			emitCallHelper(reinterpret_cast<uint64_t>(&jit_memory_lr));
		}
		emitMemoryFail(pc);
		emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, scratch)));
		storeRegister(rd, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_flw:
	case op_fld: {
		const uint32_t width = instruction.op == op_flw ? 4 : 8;
		JitLabel access;
		emitTLBLookup(rs1, static_cast<int32_t>(immediate), width, false, pc, access);
		emitter.bind(access);
		emitter.mov_rr(x64_rdx, x64_rax);
		emitter.and_imm32(x64_rdx, 4095);
		emitter.add_rr(x64_r11, x64_rdx);
		if (width == 4) {
			emitter.mov_rm(x64_rax, x64_r11, 0, false);
			emitter.mov_ri64(x64_r10, UINT64_C(0xFFFFFFFF00000000));
			emitter.or_rr(x64_rax, x64_r10);
		} else {
			emitter.mov_rm(x64_rax, x64_r11, 0);
		}
		emitter.mov_rm(x64_rdx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, floats)));
		emitter.mov_mr(x64_rdx, static_cast<int32_t>(rd) * 8, x64_rax);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_fsw:
	case op_fsd: {
		const uint32_t width = instruction.op == op_fsw ? 4 : 8;
		emitter.mov_rm(x64_r9, x64_rbp,
		               static_cast<int32_t>(offsetof(JitCall, reservation)));
		emitter.mov_rm(x64_rdx, x64_r9, 24);
		emitter.test_rr(x64_rdx, x64_rdx);
		emitter.jcc(addFailureStub(pc), x64_ne);
		JitLabel access;
		emitTLBLookup(rs1, static_cast<int32_t>(immediate), width, true, pc, access);
		emitter.bind(access);
		emitter.mov_rr(x64_rdx, x64_rax);
		emitter.and_imm32(x64_rdx, 4095);
		emitter.add_rr(x64_r11, x64_rdx);
		emitter.mov_rm(x64_rdx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, floats)));
		emitter.mov_rm(x64_r10, x64_rdx, static_cast<int32_t>(rs2) * 8);
		if (width == 4) {
			emitter.mov_m32(x64_r11, 0, x64_r10);
		} else {
			emitter.mov_mr(x64_r11, 0, x64_r10);
		}
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_fadd:
	case op_fsub:
	case op_fmul:
	case op_fdiv:
	case op_fsqrt: {
		const bool single = (instruction.imm & 1) != 0;
		const uint32_t rm = static_cast<uint32_t>(instruction.imm >> 1) & 0x7;
		const uint64_t spec =
		    fp_spec(instruction.op, rd, rs1, rs2, single, rm, 0);
		emitter.mov_rr(x64_rdi, x64_rbp);
		emitter.mov_ri64(x64_rsi, spec);
		emitCallHelper(reinterpret_cast<uint64_t>(&jit_execute_fp));
		emitMemoryFail(pc);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_fp_raw:
	case op_fp_csr: {
		const uint32_t raw = static_cast<uint32_t>(instruction.imm);
		const bool csr = instruction.op == op_fp_csr;
		const uint32_t group = raw >> 26;
		const bool integer_source = csr ? ((raw >> 12) & 7) < 5 : group == 0x3c || group == 0x34;
		if (integer_source && host_map[rs1] >= 0) {
			emitter.mov_mr(x64_r14, static_cast<int32_t>(rs1) * 8, host_map[rs1]);
		}
		emitter.mov_rr(x64_rdi, x64_rbp);
		emitter.mov_ri64(x64_rsi, raw);
		emitCallHelper(reinterpret_cast<uint64_t>(csr ? &jit_execute_fp_csr : &jit_execute_fp_raw));
		emitMemoryFail(pc);
		if (csr || group == 0x30 || group == 0x38) {
			reloadRegister(rd);
		}
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_feq:
	case op_flt:
	case op_fle: {
		const bool single = (instruction.imm & 0x4) != 0;
		const uint64_t spec =
		    fp_spec(instruction.op, rd, rs1, rs2, single, 0, 0);
		emitter.mov_rr(x64_rdi, x64_rbp);
		emitter.mov_ri64(x64_rsi, spec);
		emitCallHelper(reinterpret_cast<uint64_t>(&jit_execute_fp));
		emitMemoryFail(pc);
		reloadRegister(rd);
		emitStep();
		emitNextPC(slot);
		return;
	}
	case op_fcvt_from_int: {
		const uint32_t selector = static_cast<uint32_t>(instruction.imm) & 0x3;
		const bool single = (instruction.imm & 0x8) != 0;
		const uint32_t rm = static_cast<uint32_t>(instruction.imm >> 4) & 0x7;
		const uint64_t spec =
		    fp_spec(instruction.op, rd, rs1, rs2, single, rm, selector);
		loadInto(x64_rax, rs1);
		emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, scratch)), x64_rax);
		emitter.mov_rr(x64_rdi, x64_rbp);
		emitter.mov_ri64(x64_rsi, spec);
		emitCallHelper(reinterpret_cast<uint64_t>(&jit_execute_fp));
		emitMemoryFail(pc);
		emitStep();
		emitNextPC(slot);
		return;
	}
	default:
		emitConst(pc);
		emitter.jmp(exit_fallback);
		return;
	}
}

void JitCompiler::emitPrologue() {
	emitter.push(x64_rbp);
	emitter.mov_rr(x64_rbp, x64_rdi);
	emitter.push(x64_r15);
	emitter.push(x64_r14);
	for (uint8_t guest = 1; guest < 32; guest++) {
		if (host_map[guest] >= 0) {
			emitter.push(host_map[guest]);
		}
	}
	if ((mapped_count & 1) != 0) {
		emitter.sub_imm(x64_rsp, 8);
	}
	emitter.mov_rm(x64_r15, x64_rbp, static_cast<int32_t>(offsetof(JitCall, steps)));
	emitter.mov_rm(x64_r14, x64_rbp, static_cast<int32_t>(offsetof(JitCall, registers)));
	for (uint8_t guest = 1; guest < 32; guest++) {
		if (host_map[guest] >= 0) {
			emitter.mov_rm(host_map[guest], x64_r14, static_cast<int32_t>(guest) * 8);
		}
	}
	emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)));
	emitter.mov_rr(x64_rdx, x64_rax);
	emitter.mov_ri64(x64_rsi, base);
	emitter.sub_rr(x64_rdx, x64_rsi);
	emitter.shift_imm(x64_rdx, 5, 1);
	emitter.mov_rm(x64_rcx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, entries)));
	emitter.jmp_indexed(x64_rcx, x64_rdx, 3);
}

void JitCompiler::emitEpilogueBody(bool with_return) {
	if ((mapped_count & 1) != 0) {
		emitter.add_imm(x64_rsp, 8);
	}
	for (uint8_t guest = 31; guest >= 1; guest--) {
		if (host_map[guest] >= 0) {
			emitter.pop(host_map[guest]);
		}
	}
	emitter.pop(x64_r14);
	emitter.pop(x64_r15);
	emitter.pop(x64_rbp);
	if (with_return) {
		emitter.ret();
	}
}

void JitCompiler::emitEpilogue() {
	emitEpilogueBody(true);
}

void JitCompiler::emitLinkStub() {
	emitter.bind(link_stub);
	if (jit_links_disabled()) {
		emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)));
		emitter.jmp(exit_clean);
		return;
	}
	// 命中时当前页的寄存器已写回 JitCall，展开当前栈帧后直接进入
	// 目标页函数；目标函数会用自己的 prologue 重新装载寄存器映射。
	emitter.mov_rm(x64_rdx, x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)));
	emitter.mov_rr(x64_rcx, x64_rdx);
	emitter.shr_imm(x64_rcx, 7);
	emitter.xor_rr(x64_rcx, x64_rdx);
	emitter.and_imm(x64_rcx, jit_link_entries - 1);
	emitter.cmp_rm_indexed(x64_rdx, x64_rbp, x64_rcx, 3,
	                       static_cast<int32_t>(offsetof(JitCall, link_targets)));
	emitter.jcc(link_miss, x64_ne);
	emitter.mov_rm_indexed(x64_r10, x64_rbp, x64_rcx, 3,
	                       static_cast<int32_t>(offsetof(JitCall, link_entries)));
	emitter.mov_rm_indexed(x64_r11, x64_rbp, x64_rcx, 3,
	                       static_cast<int32_t>(offsetof(JitCall, link_functions)));
	emitter.mov_mi32(x64_rbp, static_cast<int32_t>(offsetof(JitCall, status)),
	                 jit_status_clean);
	emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, steps)), x64_r15);
	emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, entries)), x64_r10);
	for (uint8_t guest = 1; guest < 32; guest++) {
		if (host_map[guest] >= 0 && dirty[guest]) {
			emitter.mov_mr(x64_r14, static_cast<int32_t>(guest) * 8, host_map[guest]);
		}
	}
	emitter.mov_rr(x64_rdi, x64_rbp);
	emitEpilogueBody(false);
	emitter.jmp_reg(x64_r11);
	emitter.bind(link_miss);
	emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)));
	emitter.jmp(exit_clean);
}

JitPage *JitCompiler::compile() {
	scanInstructions();
	chooseRegisters();
	emitPrologue();

	compiling_primary = true;
	for (uint32_t slot = 0; slot < slots; slot++) {
		if (!primary[slot]) {
			continue;
		}
		emitter.bind(labels[slot]);
		emitInstruction(slot, decoded[slot]);
	}

	for (FailureStub &stub : failure_stubs) {
		emitter.bind(stub.label);
		emitConst(stub.pc);
		emitter.jmp(exit_fallback);
	}
	emitLinkStub();

	emitter.bind(entry_fallback);
	emitter.mov_rm(x64_rax, x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)));
	emitter.jmp(exit_fallback);

	emitter.bind(exit_clean);
	emitter.mov_mi32(x64_rbp, static_cast<int32_t>(offsetof(JitCall, status)), jit_status_clean);
	emitter.jmp(epilogue);
	emitter.bind(exit_fallback);
	emitter.mov_mi32(x64_rbp, static_cast<int32_t>(offsetof(JitCall, status)), jit_status_fallback);
	emitter.jmp(epilogue);
	emitter.bind(exit_budget);
	emitter.mov_mi32(x64_rbp, static_cast<int32_t>(offsetof(JitCall, status)), jit_status_budget);
	emitter.jmp(epilogue);

	emitter.bind(epilogue);
	emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, pc)), x64_rax);
	emitter.mov_mr(x64_rbp, static_cast<int32_t>(offsetof(JitCall, steps)), x64_r15);
	emitter.mov_rr(x64_rax, x64_r15);
	for (uint8_t guest = 1; guest < 32; guest++) {
		if (host_map[guest] >= 0 && dirty[guest]) {
			emitter.mov_mr(x64_r14, static_cast<int32_t>(guest) * 8, host_map[guest]);
		}
	}
	emitEpilogue();

	if (emitter.code.size() > jit_max_code_bytes) {
		return nullptr;
	}
	while ((emitter.code.size() & 7) != 0) {
		emitter.emit8(0x90);
	}
	const size_t entries_offset = emitter.code.size();
	emitter.code.resize(emitter.code.size() + static_cast<size_t>(slots) * 8, 0);

	const size_t mapping_size =
	    (emitter.code.size() + static_cast<size_t>(getpagesize()) - 1) &
	    ~(static_cast<size_t>(getpagesize()) - 1);
	void *mapping = mmap(nullptr, mapping_size, PROT_READ | PROT_WRITE,
	                     MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (mapping == MAP_FAILED) {
		return nullptr;
	}
	auto *bytes = static_cast<uint8_t *>(mapping);
	std::memcpy(bytes, emitter.code.data(), emitter.code.size());
	auto *entries = reinterpret_cast<uint64_t *>(bytes + entries_offset);
	for (uint32_t slot = 0; slot < slots; slot++) {
		if (starts[slot] && labels[slot].position != SIZE_MAX) {
			entries[slot] = reinterpret_cast<uint64_t>(bytes + labels[slot].position);
		} else if (entry_fallback.position != SIZE_MAX) {
			entries[slot] = reinterpret_cast<uint64_t>(bytes + entry_fallback.position);
		}
	}
	__builtin___clear_cache(reinterpret_cast<char *>(mapping),
	                        reinterpret_cast<char *>(bytes + emitter.code.size()));
	if (mprotect(mapping, mapping_size, PROT_READ | PROT_EXEC) != 0) {
		munmap(mapping, mapping_size);
		return nullptr;
	}
	auto *page = new JitPage();
	page->function = reinterpret_cast<JitPageFunction>(bytes);
	page->entries = entries;
	page->mapping = mapping;
	page->mapping_size = mapping_size;
	return page;
}

static JitPage *jit_compile_page(uint64_t identity, uint64_t version, uint64_t base,
                                 uint32_t slots, const DecodedInstruction *decoded,
                                 uint32_t start_slot) {
	if (slots != 2048 || start_slot >= slots || decoded == nullptr) {
		return nullptr;
	}
	const JitPageKey key{identity, version, base, start_slot};
	bool compile = false;
	if (JitPage *cached = jit_cache().lookup(key, compile)) {
		return cached;
	}
	if (!compile) {
		return nullptr;
	}
	JitCompiler compiler(base, slots, decoded, start_slot);
	JitPage *page = compiler.compile();
	if (page == nullptr) {
		return nullptr;
	}
	JitPage *existing = jit_cache().insert(key, page);
	if (existing == page) {
		return page;
	}
	munmap(page->mapping, page->mapping_size);
	delete page;
	return existing;
}

// Fuse the RV64 counted binary64 reduction emitted by TurboFan. The byte
// poll is invariant only while the caller owns the locked RAM view. Stop
// at the loop header, leaving both exit branches for normal execution.
static uint64_t jit_sqrt_reduction(JitCall &call, const CodeView &code, uint32_t slot,
                                   uint64_t budget, uint64_t &pc) {
	const auto *d = code.decoded;
	int adjustment = 0;
	if (d[slot].op == op_fcvt_from_int && slot >= 2) {
		slot -= 2;
		adjustment = -1; // the entry comparison has already retired
	} else if (d[slot].op == op_jal && d[slot].imm == -28 && slot >= 14) {
		slot -= 14;
		adjustment = 1; // retire the pending back edge first
	}
	if (slot + 14 >= code.slots || d[slot].op != op_bge || !call.floats || !call.fcsr) {
		return 0;
	}
	const auto *loop = d + slot;
	const uint8_t index = loop[0].rs1, limit = loop[0].rs2;
	const uint8_t temp = loop[2].rd, sum = loop[8].rd;
	const auto &poll = loop[10];
	if (loop[2].op != op_fcvt_from_int || loop[2].imm != 0 || loop[2].rs1 != index ||
	    loop[4].op != op_fsqrt || loop[4].imm != 0 || loop[4].rd != temp || loop[4].rs1 != temp ||
	    loop[6].op != op_addiw || loop[6].imm != 1 || loop[6].rs1 != index || loop[6].rd != index ||
	    loop[8].op != op_fadd || loop[8].imm != 0 || loop[8].rs1 != sum || loop[8].rs2 != temp ||
	    sum == temp || poll.op != op_lbu || loop[12].op != op_bne || loop[12].rs1 != poll.rd ||
	    loop[12].rs2 != 0 || loop[14].op != op_jal || loop[14].rd != 0 || loop[14].imm != -28 ||
	    index == 0 || index == limit || poll.rd == 0 || poll.rd == index || poll.rd == limit ||
	    poll.rs1 == index || poll.rs1 == poll.rd) {
		return 0;
	}
	for (int i = 0; i < 16; i += 2) {
		if (loop[i].length != 4) {
			return 0;
		}
	}
	const uint64_t first = call.registers[index], bound = call.registers[limit];
	// Positive bounded operands can only raise NX, already sticky here.
	const uint64_t bits = call.floats[sum];
	if (first >= bound || bound > INT32_MAX || bits < (UINT64_C(1) << 52) ||
	    bits >= (UINT64_C(0x7fd) << 52) || (*call.fcsr & 1) == 0 ||
	    std::fegetround() != FE_TONEAREST) {
		return 0;
	}
	const uint64_t count = std::min(bound - first, (budget - adjustment) / 8);
	if (count == 0) {
		return 0;
	}
	const uint8_t *flag = call.memory->translate(call.registers[poll.rs1] + poll.imm, 1,
	                                            false, code.page_base);
	if (!flag || *flag != 0) {
		return 0;
	}
	double total, root = 0;
	std::memcpy(&total, &bits, sizeof(total));
	int32_t i = static_cast<int32_t>(first);
	const int32_t end = static_cast<int32_t>(first + count);
	// Independent roots can run together; additions retain guest order.
	for (; end - i >= 2; i += 2) {
		const __m128d roots = _mm_sqrt_pd(_mm_cvtepi32_pd(_mm_set_epi32(0, 0, i + 1, i)));
		total += _mm_cvtsd_f64(roots);
		root = _mm_cvtsd_f64(_mm_unpackhi_pd(roots, roots));
		total += root;
	}
	if (i < end) {
		root = std::sqrt(static_cast<double>(i));
		total += root;
	}
	std::memcpy(&call.floats[sum], &total, sizeof(total));
	std::memcpy(&call.floats[temp], &root, sizeof(root));
	call.registers[index] = first + count;
	call.registers[poll.rd] = 0;
	pc = code.page_base + slot * 2;
	return count * 8 + adjustment;
}

static uint64_t jit_run(MemoryView &memory, CodeView &code, uint64_t identity,
                        uint64_t *registers, uint64_t current, uint32_t max_steps,
                        uint64_t retired_base, uint64_t *result_pc, uint64_t *reservation,
                        uint64_t *floats, uint64_t *fcsr) {
	if (jit_disabled() || max_steps < jit_max_straight_run) {
		return 0;
	}
	JitCall call{};
	for (JitTLBEntry &entry : call.tlb) {
		entry.page = UINT64_MAX;
	}
	for (uint64_t &page : call.inline_pages) {
		page = UINT64_MAX;
	}
	for (uint64_t &target : call.link_targets) {
		target = UINT64_MAX;
	}
	call.registers = registers;
	call.retired_base = retired_base;
	call.memory = &memory;
	call.reservation = reservation;
	call.floats = floats;
	call.fcsr = fcsr;
	call.last_page = UINT64_MAX;
	call.second_page = UINT64_MAX;
	uint64_t retired = 0;
	while (retired < max_steps) {
		const uint64_t remaining = static_cast<uint64_t>(max_steps) - retired;
		if (remaining < jit_max_straight_run || (current & 1) != 0) {
			break;
		}
		const uint64_t page = current / code.page_size;
		if (current < code.page_base ||
		    current >= code.page_base + static_cast<uint64_t>(code.slots) * 2) {
			if (!code.select(page)) {
				break;
			}
		}
		const uint64_t base = code.page_base;
		const uint32_t slots = code.slots;
		if ((current & 1) != 0) {
			break;
		}
		const uint32_t entry_slot = static_cast<uint32_t>((current - base) >> 1);
		if (entry_slot >= slots) {
			break;
		}
		const uint64_t fused = jit_sqrt_reduction(call, code, entry_slot, remaining, current);
		if (fused != 0) {
			retired += fused;
			continue;
		}
		JitPage *compiled = jit_compile_page(identity, code.page_version, base, slots,
		                                    code.decoded, code.page_start_slot);
		if (compiled == nullptr) {
			break;
		}
		jit_link_fill(&call, current, compiled);
		call.steps = 0;
		call.pc = current;
		call.entries = compiled->entries;
		call.threshold = remaining - jit_max_straight_run + 1;
		const uint64_t steps = compiled->function(&call);
		if (steps == 0 || call.steps == 0) {
			break;
		}
		retired += call.steps;
		current = call.pc;
		if (call.status != jit_status_clean) {
			break;
		}
	}
	if (retired > 0) {
		*result_pc = current;
	}
	return retired;
}

} // namespace

#else

static uint64_t jit_run(MemoryView &, CodeView &, uint64_t, uint64_t *,
                        uint64_t current, uint32_t, uint64_t, uint64_t *,
                        uint64_t *, uint64_t *, uint64_t *) {
	return 0;
}

#endif
