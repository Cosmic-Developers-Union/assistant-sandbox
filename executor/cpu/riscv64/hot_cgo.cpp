//go:build cgo && linux

#include "hot_cpp.h"

#include <algorithm>
#include <cfenv>
#include <cmath>
#include <cstdlib>
#include <cstring>
#include <limits>
#include <mutex>
#include <sys/mman.h>
#include <unistd.h>
#include <unordered_map>
#include <vector>

#pragma STDC FENV_ACCESS ON

namespace {

constexpr uint64_t flag_zero = 0;
constexpr uint64_t region_backend_flag = 1 << 8;
constexpr uint64_t prot_read = 1;
constexpr uint64_t prot_write = 2;
constexpr uint64_t prot_exec = 4;

inline int64_t sign_extend(uint64_t value, unsigned bits) {
	const uint64_t shift = 64 - bits;
	return static_cast<int64_t>(value << shift) >> shift;
}

inline void write_register(uint64_t *registers, uint32_t index, uint64_t value) {
	if (index != flag_zero) {
		registers[index] = value;
	}
}

inline uint64_t divide_signed(uint64_t left, uint64_t right) {
	const int64_t dividend = static_cast<int64_t>(left);
	const int64_t divisor = static_cast<int64_t>(right);
	if (divisor == 0) {
		return UINT64_MAX;
	}
	if (dividend == INT64_MIN && divisor == -1) {
		return static_cast<uint64_t>(INT64_MIN);
	}
	return static_cast<uint64_t>(dividend / divisor);
}

inline uint64_t divide_unsigned(uint64_t left, uint64_t right) {
	return right == 0 ? UINT64_MAX : left / right;
}

inline uint64_t remainder_signed(uint64_t left, uint64_t right) {
	const int64_t dividend = static_cast<int64_t>(left);
	const int64_t divisor = static_cast<int64_t>(right);
	if (divisor == 0) {
		return left;
	}
	if (dividend == INT64_MIN && divisor == -1) {
		return 0;
	}
	return static_cast<uint64_t>(dividend % divisor);
}

inline uint64_t remainder_unsigned(uint64_t left, uint64_t right) {
	return right == 0 ? left : left % right;
}

inline uint64_t high_signed(uint64_t left, uint64_t right) {
	const __int128 product = static_cast<__int128>(static_cast<int64_t>(left)) *
	                         static_cast<__int128>(static_cast<int64_t>(right));
	return static_cast<uint64_t>(static_cast<unsigned __int128>(product) >> 64);
}

inline uint64_t high_signed_unsigned(uint64_t left, uint64_t right) {
	const __int128 product = static_cast<__int128>(static_cast<int64_t>(left)) *
	                         static_cast<__int128>(right);
	return static_cast<uint64_t>(static_cast<unsigned __int128>(product) >> 64);
}

inline uint64_t high_unsigned(uint64_t left, uint64_t right) {
	const unsigned __int128 product = static_cast<unsigned __int128>(left) * right;
	return static_cast<uint64_t>(product >> 64);
}

inline uint64_t divide_signed_word(uint32_t left, uint32_t right) {
	const int32_t dividend = static_cast<int32_t>(left);
	const int32_t divisor = static_cast<int32_t>(right);
	if (divisor == 0) {
		return UINT32_MAX;
	}
	if (dividend == INT32_MIN && divisor == -1) {
		return static_cast<uint32_t>(INT32_MIN);
	}
	return static_cast<uint32_t>(dividend / divisor);
}

inline uint64_t divide_unsigned_word(uint32_t left, uint32_t right) {
	return right == 0 ? UINT32_MAX : left / right;
}

inline uint64_t remainder_signed_word(uint32_t left, uint32_t right) {
	const int32_t dividend = static_cast<int32_t>(left);
	const int32_t divisor = static_cast<int32_t>(right);
	if (divisor == 0) {
		return left;
	}
	if (dividend == INT32_MIN && divisor == -1) {
		return 0;
	}
	return static_cast<uint32_t>(dividend % divisor);
}

inline uint64_t remainder_unsigned_word(uint32_t left, uint32_t right) {
	return right == 0 ? left : left % right;
}

inline uint64_t apply_amo(uint64_t old_value, uint64_t operand, uint32_t funct5, bool word) {
	if (word) {
		const uint32_t old_word = static_cast<uint32_t>(old_value);
		const uint32_t operand_word = static_cast<uint32_t>(operand);
		switch (funct5) {
		case 0x00:
			return static_cast<uint64_t>(old_word + operand_word);
		case 0x01:
			return operand_word;
		case 0x04:
			return old_word ^ operand_word;
		case 0x08:
			return old_word | operand_word;
		case 0x0C:
			return old_word & operand_word;
		case 0x10:
			return static_cast<uint64_t>(std::min(static_cast<int32_t>(old_word),
			                                      static_cast<int32_t>(operand_word)));
		case 0x14:
			return static_cast<uint64_t>(std::max(static_cast<int32_t>(old_word),
			                                      static_cast<int32_t>(operand_word)));
		case 0x18:
			return std::min(old_word, operand_word);
		case 0x1C:
			return std::max(old_word, operand_word);
		default:
			return old_word;
		}
	}
	switch (funct5) {
	case 0x00:
		return old_value + operand;
	case 0x01:
		return operand;
	case 0x04:
		return old_value ^ operand;
	case 0x08:
		return old_value | operand;
	case 0x0C:
		return old_value & operand;
	case 0x10:
		return static_cast<uint64_t>(std::min(static_cast<int64_t>(old_value),
		                                      static_cast<int64_t>(operand)));
	case 0x14:
		return static_cast<uint64_t>(std::max(static_cast<int64_t>(old_value),
		                                      static_cast<int64_t>(operand)));
	case 0x18:
		return std::min(old_value, operand);
	case 0x1C:
		return std::max(old_value, operand);
	default:
		return old_value;
	}
}

// reservation 布局:[地址, 宽度, 最近 LR 值, active]。
inline void invalidate_reservation(uint64_t *reservation, uint64_t address, uint32_t width) {
	if (reservation == nullptr || reservation[3] == 0) {
		return;
	}
	const uint64_t reserved_end = reservation[0] + reservation[1];
	const uint64_t write_end = address + width;
	if (address < reserved_end && reservation[0] < write_end) {
		reservation[3] = 0;
	}
}

inline uint64_t load_value(const uint8_t *raw, uint32_t width, bool is_signed) {
	uint64_t value = 0;
#if __BYTE_ORDER__ == __ORDER_LITTLE_ENDIAN__
	switch (width) {
	case 1:
		value = raw[0];
		break;
	case 2: {
		uint16_t value16;
		__builtin_memcpy(&value16, raw, sizeof(value16));
		value = value16;
		break;
	}
	case 4: {
		uint32_t value32;
		__builtin_memcpy(&value32, raw, sizeof(value32));
		value = value32;
		break;
	}
	case 8:
		__builtin_memcpy(&value, raw, sizeof(value));
		break;
	default:
		break;
	}
#else
	for (uint32_t index = 0; index < width; index++) {
		value |= static_cast<uint64_t>(raw[index]) << (index * 8);
	}
#endif
	if (is_signed) {
		switch (width) {
		case 1:
			return static_cast<uint64_t>(static_cast<int64_t>(static_cast<int8_t>(value)));
		case 2:
			return static_cast<uint64_t>(static_cast<int64_t>(static_cast<int16_t>(value)));
		case 4:
			return static_cast<uint64_t>(static_cast<int64_t>(static_cast<int32_t>(value)));
		default:
			break;
		}
	}
	return value;
}

struct MemoryView {
	const uint64_t *regions;
	uint32_t region_count;
	const uint64_t *page_numbers;
	const uint64_t *page_addresses;
	uint32_t page_count;
	uint64_t page_size;
	uint32_t page_shift;
	uint64_t tlb_pages[64] = {};
	const uint8_t *tlb_pointers[64] = {};
	mutable int last_region = -1;
	struct FastPage {
		uint64_t page = UINT64_MAX;
		uint8_t *pointer = nullptr;
		uint64_t flags = 0;
	};
	FastPage fast_pages[4];

	MemoryView(const uint64_t *regions_value, uint32_t region_count_value,
	           const uint64_t *page_numbers_value, const uint64_t *page_addresses_value,
	           uint32_t page_count_value, uint64_t page_size_value)
	    : regions(regions_value), region_count(region_count_value),
	      page_numbers(page_numbers_value), page_addresses(page_addresses_value),
	      page_count(page_count_value), page_size(page_size_value),
	      page_shift(static_cast<uint32_t>(__builtin_ctzll(page_size_value))) {}

	int find_region(uint64_t address) const {
		if (last_region >= 0 && regions[last_region * 3] <= address &&
		    address < regions[last_region * 3 + 1]) {
			return last_region;
		}
		uint32_t low = 0;
		uint32_t high = region_count;
		while (low < high) {
			const uint32_t middle = low + (high - low) / 2;
			if (regions[middle * 3 + 1] <= address) {
				low = middle + 1;
			} else {
				high = middle;
			}
		}
		if (low < region_count && regions[low * 3] <= address) {
			last_region = static_cast<int>(low);
			return static_cast<int>(low);
		}
		return -1;
	}

	const uint8_t *find_page(uint64_t page) {
		const uint32_t slot = static_cast<uint32_t>((page ^ (page >> 10)) & 63);
		// 存 page+1，零值表示未命中；直接映射冲突时由二分查找重建。
		if (tlb_pages[slot] == page + 1) {
			return tlb_pointers[slot];
		}
		uint32_t low = 0;
		uint32_t high = page_count;
		while (low < high) {
			const uint32_t middle = low + (high - low) / 2;
			if (page_numbers[middle] < page) {
				low = middle + 1;
			} else {
				high = middle;
			}
		}
		const uint8_t *pointer = nullptr;
		if (low < page_count && page_numbers[low] == page) {
			pointer = reinterpret_cast<const uint8_t *>(static_cast<uintptr_t>(page_addresses[low]));
		}
		tlb_pages[slot] = page + 1;
		tlb_pointers[slot] = pointer;
		return pointer;
	}

	uint8_t *translate(uint64_t address, uint32_t width, bool write_access, uint64_t code_page_base,
	                   uint64_t *flags_out = nullptr) {
		const uint64_t page = address >> page_shift;
		const uint64_t page_end = (page + 1) << page_shift;
		if (address + width > page_end) {
			return nullptr;
		}
		FastPage &fast = fast_pages[(page ^ (page >> 7)) & 3];
		if (fast.page == page) {
			if (write_access) {
				if ((fast.flags & prot_write) == 0 || (fast.flags & prot_exec) != 0 ||
				    page == (code_page_base >> page_shift)) {
					return nullptr;
				}
			} else if ((fast.flags & prot_read) == 0) {
				return nullptr;
			}
			if (flags_out != nullptr) {
				*flags_out = fast.flags;
			}
			return fast.pointer + (address & (page_size - 1));
		}
		const int region_index = find_region(address);
		if (region_index < 0) {
			return nullptr;
		}
		const uint64_t flags = regions[region_index * 3 + 2];
		if ((flags & region_backend_flag) != 0) {
			return nullptr;
		}
		if (write_access) {
			if ((flags & prot_write) == 0 || (flags & prot_exec) != 0 ||
			    page == (code_page_base >> page_shift)) {
				return nullptr;
			}
		} else if ((flags & prot_read) == 0) {
			return nullptr;
		}
		uint8_t *pointer = const_cast<uint8_t *>(find_page(page));
		if (pointer == nullptr) {
			static const uint8_t zero_page[4096] = {};
			if (flags_out != nullptr) {
				*flags_out = flags;
			}
			return write_access ? nullptr : const_cast<uint8_t *>(zero_page);
		}
		if (flags_out != nullptr) {
			*flags_out = flags;
		}
		fast.page = page;
		fast.pointer = pointer;
		fast.flags = flags;
		return pointer + (address & (page_size - 1));
	}
};

enum Op : uint32_t {
	op_unsupported = 0,
	op_lui,
	op_auipc,
	op_jal,
	op_jalr,
	op_beq,
	op_bne,
	op_blt,
	op_bge,
	op_bltu,
	op_bgeu,
	op_lb,
	op_lh,
	op_lw,
	op_ld,
	op_lbu,
	op_lhu,
	op_lwu,
	op_sb,
	op_sh,
	op_sw,
	op_sd,
	op_addi,
	op_slti,
	op_sltiu,
	op_xori,
	op_ori,
	op_andi,
	op_slli,
	op_srli,
	op_srai,
	op_addiw,
	op_slliw,
	op_srliw,
	op_sraiw,
	op_add,
	op_sub,
	op_sll,
	op_slt,
	op_sltu,
	op_xor,
	op_srl,
	op_sra,
	op_or,
	op_and,
	op_addw,
	op_subw,
	op_sllw,
	op_srlw,
	op_sraw,
	op_mul,
	op_mulh,
	op_mulhsu,
	op_mulhu,
	op_div,
	op_divu,
	op_rem,
	op_remu,
	op_mulw,
	op_divw,
	op_divuw,
	op_remw,
	op_remuw,
	op_lrsc,
	op_amo,
	op_flw,
	op_fld,
	op_fsw,
	op_fsd,
	op_fadd,
	op_fsub,
	op_fmul,
	op_fdiv,
	op_fsqrt,
	op_fcvt_from_int,
	op_feq,
	op_flt,
	op_fle,
	op_fence,
	op_rdtime,
	op_fp_raw,
	op_fp_csr,
	op_count
};

struct DecodedInstruction {
	uint32_t op;
	int32_t imm;
	uint8_t rd;
	uint8_t rs1;
	uint8_t rs2;
	uint8_t length;
};

static_assert(sizeof(DecodedInstruction) == 12, "decoded instruction 布局与 Go 不一致");

struct CodeView {
	const uint64_t *bases;
	const uint64_t *decoded_pointers;
	const uint64_t *versions;
	const uint64_t *start_slots;
	uint32_t count;
	uint64_t page_size;
	uint64_t page_base;
	uint64_t page_version;
	uint32_t page_start_slot;
	uint32_t slots;
	const DecodedInstruction *decoded;

	CodeView(const uint64_t *bases_value, const uint64_t *decoded_pointers_value,
	         const uint64_t *versions_value, const uint64_t *start_slots_value,
	         uint32_t count_value, uint64_t page_size_value)
	    : bases(bases_value), decoded_pointers(decoded_pointers_value),
	      versions(versions_value), start_slots(start_slots_value), count(count_value),
	      page_size(page_size_value), page_base(0), page_version(0), page_start_slot(0),
	      slots(0), decoded(nullptr) {}

	bool select(uint64_t page) {
		if (count == 0 || (count & (count - 1)) != 0 ||
		    bases == nullptr || decoded_pointers == nullptr || versions == nullptr ||
		    start_slots == nullptr) {
			return false;
		}
		const uint32_t slot = static_cast<uint32_t>((page ^ (page >> 7)) & (count - 1));
		if (bases[slot] != page * page_size || decoded_pointers[slot] == 0) {
			return false;
		}
		page_base = bases[slot];
		page_version = versions[slot];
		page_start_slot = static_cast<uint32_t>(start_slots[slot] & 1);
		slots = static_cast<uint32_t>(page_size / 2);
		decoded = reinterpret_cast<const DecodedInstruction *>(
		    static_cast<uintptr_t>(decoded_pointers[slot]));
		return true;
	}

	bool contains_page(uint64_t page) const {
		if (count == 0 || bases == nullptr) {
			return false;
		}
		const uint32_t slot = static_cast<uint32_t>((page ^ (page >> 7)) & (count - 1));
		return bases[slot] == page * page_size;
	}
};

#include "hot_jit.h"

} // namespace

extern "C" uint64_t rv64_hot_run(uint64_t *registers, uint64_t *pc, uint64_t page_base,
                                  uint64_t jit_identity, uint32_t max_steps,
                                  uint64_t retired_base,
                                  const uint64_t *regions, uint32_t region_count,
                                  const uint64_t *page_numbers, const uint64_t *page_addresses,
                                  uint32_t page_count, uint64_t page_size,
                                  const uint64_t *code_bases, const uint64_t *code_decoded,
                                  const uint64_t *code_versions, const uint64_t *code_start_slots,
                                  uint32_t code_page_count,
                                  uint64_t instruction_page_size, uint64_t *reservation,
                                  uint64_t *floats, uint64_t *fcsr) {
	if (registers == nullptr || pc == nullptr || max_steps == 0) {
		return 0;
	}
	MemoryView memory(regions, region_count, page_numbers, page_addresses,
	                  page_count, page_size);
	CodeView code(code_bases, code_decoded, code_versions, code_start_slots, code_page_count,
	              instruction_page_size);
	if (!code.select(page_base / instruction_page_size)) {
		return 0;
	}
	uint64_t current = *pc;
	const uint64_t jit_steps =
	    jit_run(memory, code, jit_identity, registers, current, max_steps, retired_base,
	            &current, reservation, floats, fcsr);
	if (jit_steps > 0) {
		*pc = current;
		registers[0] = 0;
		return jit_steps;
	}
	uint64_t retired = 0;
	while (retired < max_steps) {
		if (current < code.page_base || current >= code.page_base + instruction_page_size) {
			if (!code.select(current / instruction_page_size)) {
				break;
			}
		}
		page_base = code.page_base;
		const uint64_t offset = current - page_base;
		if ((offset & 1) != 0) {
			break;
		}
		const uint32_t slot = static_cast<uint32_t>(offset >> 1);
		if (slot >= code.slots) {
			break;
		}
		const DecodedInstruction &decoded = code.decoded[slot];
		if (decoded.op == op_unsupported ||
		    (decoded.length != 2 && decoded.length != 4)) {
			break;
		}
		const uint64_t immediate = static_cast<uint64_t>(static_cast<int64_t>(decoded.imm));
		const uint64_t next = current + decoded.length;
		uint64_t target = next;
		bool stop = false;
		switch (decoded.op) {
		case op_lui:
			write_register(registers, decoded.rd, immediate);
			break;
		case op_auipc:
			write_register(registers, decoded.rd, current + immediate);
			break;
		case op_jal:
			write_register(registers, decoded.rd, next);
			target = current + immediate;
			break;
		case op_jalr:
			target = (registers[decoded.rs1] + immediate) & ~UINT64_C(1);
			write_register(registers, decoded.rd, next);
			break;
		case op_beq:
			if (registers[decoded.rs1] == registers[decoded.rs2]) {
				target = current + immediate;
			}
			break;
		case op_bne:
			if (registers[decoded.rs1] != registers[decoded.rs2]) {
				target = current + immediate;
			}
			break;
		case op_blt:
			if (static_cast<int64_t>(registers[decoded.rs1]) <
			    static_cast<int64_t>(registers[decoded.rs2])) {
				target = current + immediate;
			}
			break;
		case op_bge:
			if (static_cast<int64_t>(registers[decoded.rs1]) >=
			    static_cast<int64_t>(registers[decoded.rs2])) {
				target = current + immediate;
			}
			break;
		case op_bltu:
			if (registers[decoded.rs1] < registers[decoded.rs2]) {
				target = current + immediate;
			}
			break;
		case op_bgeu:
			if (registers[decoded.rs1] >= registers[decoded.rs2]) {
				target = current + immediate;
			}
			break;
		case op_lb:
		case op_lh:
		case op_lw:
		case op_ld:
		case op_lbu:
		case op_lhu:
		case op_lwu: {
			uint32_t width = 0;
			bool is_signed = false;
			switch (decoded.op) {
			case op_lb:
				width = 1;
				is_signed = true;
				break;
			case op_lh:
				width = 2;
				is_signed = true;
				break;
			case op_lw:
				width = 4;
				is_signed = true;
				break;
			case op_ld:
				width = 8;
				break;
			case op_lbu:
				width = 1;
				break;
			case op_lhu:
				width = 2;
				break;
			default:
				width = 4;
				break;
			}
			const uint64_t address = registers[decoded.rs1] + immediate;
			const uint8_t *source = memory.translate(address, width, false, code.page_base);
			if (source == nullptr) {
				stop = true;
			} else {
				write_register(registers, decoded.rd, load_value(source, width, is_signed));
			}
			break;
		}
		case op_sb:
		case op_sh:
		case op_sw:
		case op_sd: {
			uint32_t width = decoded.op == op_sb ? 1 : decoded.op == op_sh ? 2 : decoded.op == op_sw ? 4 : 8;
			const uint64_t address = registers[decoded.rs1] + immediate;
			if (code.contains_page(address >> memory.page_shift)) {
				stop = true;
				break;
			}
			uint8_t *destination = memory.translate(address, width, true, code.page_base);
			if (destination == nullptr) {
				stop = true;
				break;
			}
			const uint64_t value = registers[decoded.rs2];
#if __BYTE_ORDER__ == __ORDER_LITTLE_ENDIAN__
			__builtin_memcpy(destination, &value, width);
#else
			for (uint32_t index = 0; index < width; index++) {
				destination[index] = static_cast<uint8_t>(value >> (index * 8));
			}
#endif
			invalidate_reservation(reservation, address, width);
			break;
		}
		case op_addi:
			write_register(registers, decoded.rd, registers[decoded.rs1] + immediate);
			break;
		case op_slti:
			write_register(registers, decoded.rd,
			               static_cast<int64_t>(registers[decoded.rs1]) < static_cast<int64_t>(immediate));
			break;
		case op_sltiu:
			write_register(registers, decoded.rd, registers[decoded.rs1] < immediate);
			break;
		case op_xori:
			write_register(registers, decoded.rd, registers[decoded.rs1] ^ immediate);
			break;
		case op_ori:
			write_register(registers, decoded.rd, registers[decoded.rs1] | immediate);
			break;
		case op_andi:
			write_register(registers, decoded.rd, registers[decoded.rs1] & immediate);
			break;
		case op_slli:
			write_register(registers, decoded.rd, registers[decoded.rs1] << (decoded.imm & 0x3F));
			break;
		case op_srli:
			write_register(registers, decoded.rd, registers[decoded.rs1] >> (decoded.imm & 0x3F));
			break;
		case op_srai:
			write_register(registers, decoded.rd, static_cast<uint64_t>(
			               static_cast<int64_t>(registers[decoded.rs1]) >> (decoded.imm & 0x3F)));
			break;
		case op_addiw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1] + immediate), 32)));
			break;
		case op_slliw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1]) << (decoded.imm & 0x1F), 32)));
			break;
		case op_srliw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1]) >> (decoded.imm & 0x1F), 32)));
			break;
		case op_sraiw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(static_cast<int32_t>(registers[decoded.rs1]) >> (decoded.imm & 0x1F)), 32)));
			break;
		case op_add:
			write_register(registers, decoded.rd, registers[decoded.rs1] + registers[decoded.rs2]);
			break;
		case op_sub:
			write_register(registers, decoded.rd, registers[decoded.rs1] - registers[decoded.rs2]);
			break;
		case op_sll:
			write_register(registers, decoded.rd, registers[decoded.rs1] << (registers[decoded.rs2] & 0x3F));
			break;
		case op_slt:
			write_register(registers, decoded.rd, static_cast<int64_t>(registers[decoded.rs1]) <
			               static_cast<int64_t>(registers[decoded.rs2]));
			break;
		case op_sltu:
			write_register(registers, decoded.rd, registers[decoded.rs1] < registers[decoded.rs2]);
			break;
		case op_xor:
			write_register(registers, decoded.rd, registers[decoded.rs1] ^ registers[decoded.rs2]);
			break;
		case op_srl:
			write_register(registers, decoded.rd, registers[decoded.rs1] >> (registers[decoded.rs2] & 0x3F));
			break;
		case op_sra:
			write_register(registers, decoded.rd, static_cast<uint64_t>(
			               static_cast<int64_t>(registers[decoded.rs1]) >> (registers[decoded.rs2] & 0x3F)));
			break;
		case op_or:
			write_register(registers, decoded.rd, registers[decoded.rs1] | registers[decoded.rs2]);
			break;
		case op_and:
			write_register(registers, decoded.rd, registers[decoded.rs1] & registers[decoded.rs2]);
			break;
		case op_addw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1] + registers[decoded.rs2]), 32)));
			break;
		case op_subw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1] - registers[decoded.rs2]), 32)));
			break;
		case op_sllw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1]) << (registers[decoded.rs2] & 0x1F), 32)));
			break;
		case op_srlw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1]) >> (registers[decoded.rs2] & 0x1F), 32)));
			break;
		case op_sraw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(static_cast<int32_t>(registers[decoded.rs1]) >>
			                                     (registers[decoded.rs2] & 0x1F)), 32)));
			break;
		case op_mul:
			write_register(registers, decoded.rd, registers[decoded.rs1] * registers[decoded.rs2]);
			break;
		case op_mulh:
			write_register(registers, decoded.rd, high_signed(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_mulhsu:
			write_register(registers, decoded.rd, high_signed_unsigned(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_mulhu:
			write_register(registers, decoded.rd, high_unsigned(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_div:
			write_register(registers, decoded.rd, divide_signed(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_divu:
			write_register(registers, decoded.rd, divide_unsigned(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_rem:
			write_register(registers, decoded.rd, remainder_signed(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_remu:
			write_register(registers, decoded.rd, remainder_unsigned(registers[decoded.rs1], registers[decoded.rs2]));
			break;
		case op_mulw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               static_cast<uint32_t>(registers[decoded.rs1] * registers[decoded.rs2]), 32)));
			break;
		case op_divw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               divide_signed_word(static_cast<uint32_t>(registers[decoded.rs1]),
			                                  static_cast<uint32_t>(registers[decoded.rs2])), 32)));
			break;
		case op_divuw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               divide_unsigned_word(static_cast<uint32_t>(registers[decoded.rs1]),
			                                    static_cast<uint32_t>(registers[decoded.rs2])), 32)));
			break;
		case op_remw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               remainder_signed_word(static_cast<uint32_t>(registers[decoded.rs1]),
			                                     static_cast<uint32_t>(registers[decoded.rs2])), 32)));
			break;
		case op_remuw:
			write_register(registers, decoded.rd, static_cast<uint64_t>(sign_extend(
			               remainder_unsigned_word(static_cast<uint32_t>(registers[decoded.rs1]),
			                                       static_cast<uint32_t>(registers[decoded.rs2])), 32)));
			break;
		case op_flw:
		case op_fld: {
			const uint32_t width = decoded.op == op_flw ? 4 : 8;
			const uint64_t address = registers[decoded.rs1] + immediate;
			const uint8_t *source = memory.translate(address, width, false, code.page_base);
			if (source == nullptr || floats == nullptr) {
				stop = true;
				break;
			}
			uint64_t value = 0;
			__builtin_memcpy(&value, source, width);
			floats[decoded.rd] =
			    width == 4 ? UINT64_C(0xFFFFFFFF00000000) | static_cast<uint32_t>(value)
			               : value;
			break;
		}
		case op_fsw:
		case op_fsd: {
			const uint32_t width = decoded.op == op_fsw ? 4 : 8;
			const uint64_t address = registers[decoded.rs1] + immediate;
			uint8_t *destination = memory.translate(address, width, true, code.page_base);
			if (destination == nullptr || floats == nullptr) {
				stop = true;
				break;
			}
			const uint64_t value = floats[decoded.rs2];
			__builtin_memcpy(destination, &value, width);
			invalidate_reservation(reservation, address, width);
			break;
		}
		case op_fadd:
		case op_fsub:
		case op_fmul:
		case op_fdiv:
		case op_fsqrt: {
			const bool single = (decoded.imm & 1) != 0;
			const uint32_t rm = static_cast<uint32_t>(decoded.imm >> 1) & 0x7;
			const uint64_t spec = fp_spec(decoded.op, decoded.rd, decoded.rs1, decoded.rs2,
			                              single, rm, 0);
			if (!fp_execute(registers, floats, fcsr, spec, nullptr)) {
				stop = true;
			}
			break;
		}
		case op_feq:
		case op_flt:
		case op_fle: {
			const bool single = (decoded.imm & 0x4) != 0;
			const uint64_t spec = fp_spec(decoded.op, decoded.rd, decoded.rs1, decoded.rs2,
			                              single, 0, 0);
			if (!fp_execute(registers, floats, fcsr, spec, nullptr)) {
				stop = true;
			}
			break;
		}
		case op_fcvt_from_int: {
			const uint32_t selector = static_cast<uint32_t>(decoded.imm) & 0x3;
			const bool single = (decoded.imm & 0x8) != 0;
			const uint32_t rm = static_cast<uint32_t>(decoded.imm >> 4) & 0x7;
			const uint64_t spec = fp_spec(decoded.op, decoded.rd, decoded.rs1, decoded.rs2,
			                              single, rm, selector);
			if (!fp_execute(registers, floats, fcsr, spec, nullptr)) {
				stop = true;
			}
			break;
		}
		case op_fp_raw:
			if (!fp_execute_raw(registers, floats, fcsr, static_cast<uint32_t>(decoded.imm))) {
				stop = true;
			}
			break;
		case op_fp_csr:
			if (!fp_execute_csr(registers, fcsr, static_cast<uint32_t>(decoded.imm))) {
				stop = true;
			}
			break;
		case op_lrsc: {
			const uint32_t width = static_cast<uint32_t>(decoded.imm) & 0xFF;
			const bool store_conditional =
			    ((static_cast<uint32_t>(decoded.imm) >> 8) & 0x1F) == 0x3;
			const uint64_t address = registers[decoded.rs1];
			if ((width != 4 && width != 8) || address % width != 0) {
				stop = true;
				break;
			}
			if (store_conditional) {
				if (reservation == nullptr || reservation[3] == 0 ||
				    reservation[0] != address || reservation[1] != width) {
					write_register(registers, decoded.rd, 1);
					break;
				}
				uint8_t *destination =
				    memory.translate(address, width, true, code.page_base);
				if (destination == nullptr) {
					stop = true;
					break;
				}
				uint64_t old_value = 0;
				__builtin_memcpy(&old_value, destination, width);
				reservation[3] = 0;
				if (old_value != reservation[2]) {
					write_register(registers, decoded.rd, 1);
					break;
				}
				const uint64_t value = registers[decoded.rs2];
				__builtin_memcpy(destination, &value, width);
				write_register(registers, decoded.rd, 0);
				break;
			}
			const uint8_t *source = memory.translate(address, width, false, code.page_base);
			if (source == nullptr) {
				stop = true;
				break;
			}
			const uint64_t value = load_value(source, width, false);
			reservation[0] = address;
			reservation[1] = width;
			reservation[2] = value;
			reservation[3] = 1;
			if (width == 4) {
				write_register(registers, decoded.rd,
				               static_cast<uint64_t>(static_cast<int64_t>(
				                   static_cast<int32_t>(static_cast<uint32_t>(value)))));
			} else {
				write_register(registers, decoded.rd, value);
			}
			break;
		}
		case op_amo: {
			const uint32_t width = static_cast<uint32_t>(decoded.imm) & 0xFF;
			const uint32_t funct5 = static_cast<uint32_t>(decoded.imm) >> 8;
			const uint64_t address = registers[decoded.rs1];
			if ((width != 4 && width != 8) || address % width != 0) {
				stop = true;
				break;
			}
			uint8_t *destination = memory.translate(address, width, true, code.page_base);
			if (destination == nullptr) {
				stop = true;
				break;
			}
			uint64_t old_value = 0;
			__builtin_memcpy(&old_value, destination, width);
			const uint64_t updated =
			    apply_amo(old_value, registers[decoded.rs2], funct5, width == 4);
			__builtin_memcpy(destination, &updated, width);
			invalidate_reservation(reservation, address, width);
			if (width == 4) {
				write_register(registers, decoded.rd,
				               static_cast<uint64_t>(static_cast<int64_t>(
				                   static_cast<int32_t>(static_cast<uint32_t>(old_value)))));
			} else {
				write_register(registers, decoded.rd, old_value);
			}
			break;
		}
		case op_fence:
			break;
		case op_rdtime:
			write_register(registers, decoded.rd, retired_base + retired);
			break;
		default:
			stop = true;
			break;
		}
		if (stop) {
			break;
		}
		retired++;
		current = target;
	}
	*pc = current;
	registers[0] = 0;
	return retired;
}
