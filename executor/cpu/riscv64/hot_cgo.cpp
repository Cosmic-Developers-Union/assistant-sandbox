//go:build cgo && linux

#include "hot_cpp.h"

namespace {

constexpr uint64_t flag_zero = 0;
constexpr uint64_t region_backend_flag = 1 << 8;
constexpr uint64_t prot_read = 1;
constexpr uint64_t prot_write = 2;

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

inline uint64_t load_value(const uint8_t *raw, uint32_t width, bool is_signed) {
	uint64_t value = 0;
	for (uint32_t index = 0; index < width; index++) {
		value |= static_cast<uint64_t>(raw[index]) << (index * 8);
	}
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
	uint64_t tlb_pages[64] = {};
	const uint8_t *tlb_pointers[64] = {};

	MemoryView(const uint64_t *regions_value, uint32_t region_count_value,
	           const uint64_t *page_numbers_value, const uint64_t *page_addresses_value,
	           uint32_t page_count_value, uint64_t page_size_value)
	    : regions(regions_value), region_count(region_count_value),
	      page_numbers(page_numbers_value), page_addresses(page_addresses_value),
	      page_count(page_count_value), page_size(page_size_value) {}

	int find_region(uint64_t address) const {
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

	uint8_t *translate(uint64_t address, uint32_t width, bool write_access, uint64_t code_page_base) {
		const uint64_t page = address / page_size;
		const uint64_t page_end = (page + 1) * page_size;
		if (address + width > page_end) {
			return nullptr;
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
			if ((flags & prot_write) == 0 || page == code_page_base / page_size) {
				return nullptr;
			}
		} else if ((flags & prot_read) == 0) {
			return nullptr;
		}
		uint8_t *pointer = const_cast<uint8_t *>(find_page(page));
		if (pointer == nullptr) {
			static const uint8_t zero_page[8] = {};
			return write_access ? nullptr : const_cast<uint8_t *>(zero_page);
		}
		return pointer + (address % page_size);
	}
};

struct CodeView {
	const uint64_t *bases;
	const uint64_t *word_pointers;
	const uint64_t *length_pointers;
	uint32_t count;
	uint64_t page_size;
	uint64_t page_base;
	uint32_t slots;
	const uint32_t *words;
	const uint8_t *lengths;

	CodeView(const uint64_t *bases_value, const uint64_t *word_pointers_value,
	         const uint64_t *length_pointers_value, uint32_t count_value, uint64_t page_size_value)
	    : bases(bases_value), word_pointers(word_pointers_value),
	      length_pointers(length_pointers_value), count(count_value),
	      page_size(page_size_value), page_base(0), slots(0), words(nullptr), lengths(nullptr) {}

	bool select(uint64_t page) {
		if (count == 0 || (count & (count - 1)) != 0 ||
		    bases == nullptr || word_pointers == nullptr || length_pointers == nullptr) {
			return false;
		}
		const uint32_t slot = static_cast<uint32_t>((page ^ (page >> 7)) & (count - 1));
		if (bases[slot] != page * page_size || word_pointers[slot] == 0 || length_pointers[slot] == 0) {
			return false;
		}
		page_base = bases[slot];
		slots = static_cast<uint32_t>(page_size / 2);
		words = reinterpret_cast<const uint32_t *>(static_cast<uintptr_t>(word_pointers[slot]));
		lengths = reinterpret_cast<const uint8_t *>(static_cast<uintptr_t>(length_pointers[slot]));
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

} // namespace

extern "C" uint64_t rv64_hot_run(uint64_t *registers, uint64_t *pc, uint64_t page_base,
                                  uint32_t max_steps,
                                  const uint64_t *regions, uint32_t region_count,
                                  const uint64_t *page_numbers, const uint64_t *page_addresses,
                                  uint32_t page_count, uint64_t page_size,
                                  const uint64_t *code_bases, const uint64_t *code_words,
                                  const uint64_t *code_lengths, uint32_t code_page_count,
                                  uint64_t instruction_page_size) {
	if (registers == nullptr || pc == nullptr || max_steps == 0) {
		return 0;
	}
	MemoryView memory(regions, region_count, page_numbers, page_addresses,
	                  page_count, page_size);
	CodeView code(code_bases, code_words, code_lengths, code_page_count, instruction_page_size);
	if (!code.select(page_base / instruction_page_size)) {
		return 0;
	}
	uint64_t current = *pc;
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
		const uint32_t slot = static_cast<uint32_t>(offset / 2);
		const uint8_t length = code.lengths[slot];
		if (length != 2 && length != 4) {
			break;
		}
		const uint32_t instruction = code.words[slot];
		const uint32_t opcode = instruction & 0x7F;
		const uint32_t rd = (instruction >> 7) & 0x1F;
		const uint32_t funct3 = (instruction >> 12) & 0x7;
		const uint32_t rs1 = (instruction >> 15) & 0x1F;
		const uint32_t rs2 = (instruction >> 20) & 0x1F;
		const uint32_t funct7 = (instruction >> 25) & 0x7F;
		const uint64_t next = current + length;
		uint64_t target = next;
		bool unsupported = false;

		switch (opcode) {
		case 0x37: // LUI
			write_register(registers, rd,
			               static_cast<uint64_t>(sign_extend(instruction & 0xFFFFF000u, 32)));
			break;
		case 0x17: // AUIPC
			write_register(registers, rd,
			               current + static_cast<uint64_t>(sign_extend(instruction & 0xFFFFF000u, 32)));
			break;
		case 0x6F: { // JAL
			const uint64_t immediate =
			    static_cast<uint64_t>(instruction >> 31) << 20 |
			    static_cast<uint64_t>((instruction >> 12) & 0xFF) << 12 |
			    static_cast<uint64_t>((instruction >> 20) & 0x1) << 11 |
			    static_cast<uint64_t>((instruction >> 21) & 0x3FF) << 1;
			write_register(registers, rd, next);
			target = current + static_cast<uint64_t>(sign_extend(immediate, 21));
			break;
		}
		case 0x67: { // JALR
			if (funct3 != 0) {
				unsupported = true;
				break;
			}
			const int64_t immediate = sign_extend(instruction >> 20, 12);
			const uint64_t link = next;
			target = (registers[rs1] + static_cast<uint64_t>(immediate)) & ~UINT64_C(1);
			write_register(registers, rd, link);
			break;
		}
		case 0x63: { // branches
			const uint64_t immediate =
			    static_cast<uint64_t>(instruction >> 31) << 12 |
			    static_cast<uint64_t>((instruction >> 7) & 0x1) << 11 |
			    static_cast<uint64_t>((instruction >> 25) & 0x3F) << 5 |
			    static_cast<uint64_t>((instruction >> 8) & 0xF) << 1;
			const uint64_t left = registers[rs1];
			const uint64_t right = registers[rs2];
			bool taken = false;
			switch (funct3) {
			case 0x0:
				taken = left == right;
				break;
			case 0x1:
				taken = left != right;
				break;
			case 0x4:
				taken = static_cast<int64_t>(left) < static_cast<int64_t>(right);
				break;
			case 0x5:
				taken = static_cast<int64_t>(left) >= static_cast<int64_t>(right);
				break;
			case 0x6:
				taken = left < right;
				break;
			case 0x7:
				taken = left >= right;
				break;
			default:
				unsupported = true;
				break;
			}
			if (unsupported) {
				break;
			}
			if (taken) {
				target = current + static_cast<uint64_t>(sign_extend(immediate, 13));
			}
			break;
		}
		case 0x13: { // OP-IMM
			const uint64_t immediate = static_cast<uint64_t>(sign_extend(instruction >> 20, 12));
			const uint64_t source = registers[rs1];
			const uint32_t shift = (instruction >> 20) & 0x3F;
			const uint32_t shift_kind = (instruction >> 26) & 0x3F;
			switch (funct3) {
			case 0x0:
				write_register(registers, rd, source + immediate);
				break;
			case 0x1:
				if (shift_kind != 0) {
					unsupported = true;
				} else {
					write_register(registers, rd, source << shift);
				}
				break;
			case 0x2:
				write_register(registers, rd,
				               static_cast<int64_t>(source) < static_cast<int64_t>(immediate));
				break;
			case 0x3:
				write_register(registers, rd, source < immediate);
				break;
			case 0x4:
				write_register(registers, rd, source ^ immediate);
				break;
			case 0x5:
				if (shift_kind == 0) {
					write_register(registers, rd, source >> shift);
				} else if (shift_kind == 0x10) {
					write_register(registers, rd, static_cast<uint64_t>(static_cast<int64_t>(source) >> shift));
				} else {
					unsupported = true;
				}
				break;
			case 0x6:
				write_register(registers, rd, source | immediate);
				break;
			case 0x7:
				write_register(registers, rd, source & immediate);
				break;
			default:
				unsupported = true;
				break;
			}
			break;
		}
		case 0x03: { // loads
			uint32_t width = 0;
			bool is_signed = false;
			switch (funct3) {
			case 0x0:
				width = 1;
				is_signed = true;
				break;
			case 0x1:
				width = 2;
				is_signed = true;
				break;
			case 0x2:
				width = 4;
				is_signed = true;
				break;
			case 0x3:
				width = 8;
				break;
			case 0x4:
				width = 1;
				break;
			case 0x5:
				width = 2;
				break;
			case 0x6:
				width = 4;
				break;
			default:
				unsupported = true;
				break;
			}
			if (unsupported) {
				break;
			}
			const uint64_t address = registers[rs1] +
			                         static_cast<uint64_t>(sign_extend(instruction >> 20, 12));
			const uint8_t *source = memory.translate(address, width, false, code.page_base);
			if (source == nullptr) {
				unsupported = true;
				break;
			}
			write_register(registers, rd, load_value(source, width, is_signed));
			break;
		}
		case 0x23: { // stores
			uint32_t width = 0;
			switch (funct3) {
			case 0x0:
				width = 1;
				break;
			case 0x1:
				width = 2;
				break;
			case 0x2:
				width = 4;
				break;
			case 0x3:
				width = 8;
				break;
			default:
				unsupported = true;
				break;
			}
			if (unsupported) {
				break;
			}
			const uint64_t immediate =
			    static_cast<uint64_t>(instruction >> 25) << 5 |
			    static_cast<uint64_t>((instruction >> 7) & 0x1F);
			const uint64_t address = registers[rs1] +
			                         static_cast<uint64_t>(sign_extend(immediate, 12));
			const uint64_t value = registers[rs2];
			if (code.contains_page(address / page_size)) {
				unsupported = true;
				break;
			}
			uint8_t *destination = memory.translate(address, width, true, code.page_base);
			if (destination == nullptr) {
				unsupported = true;
				break;
			}
			for (uint32_t index = 0; index < width; index++) {
				destination[index] = static_cast<uint8_t>(value >> (index * 8));
			}
			break;
		}
		case 0x1B: { // OP-IMM-32
			const uint32_t source = static_cast<uint32_t>(registers[rs1]);
			const uint32_t immediate = static_cast<uint32_t>(sign_extend(instruction >> 20, 12));
			const uint32_t shift = (instruction >> 20) & 0x1F;
			const uint32_t shift_kind = (instruction >> 25) & 0x7F;
			switch (funct3) {
			case 0x0:
				write_register(registers, rd, static_cast<uint64_t>(
				                                   sign_extend(static_cast<uint32_t>(source + immediate), 32)));
				break;
			case 0x1:
				if (shift_kind != 0) {
					unsupported = true;
				} else {
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(source << shift, 32)));
				}
				break;
			case 0x5:
				if (shift_kind == 0) {
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(source >> shift, 32)));
				} else if (shift_kind == 0x20) {
					write_register(registers, rd, static_cast<uint64_t>(sign_extend(
					                                   static_cast<uint32_t>(static_cast<int32_t>(source) >> shift), 32)));
				} else {
					unsupported = true;
				}
				break;
			default:
				unsupported = true;
				break;
			}
			break;
		}
		case 0x33: { // OP
			const uint64_t left = registers[rs1];
			const uint64_t right = registers[rs2];
			if (funct7 == 0x00) {
				switch (funct3) {
				case 0x0:
					write_register(registers, rd, left + right);
					break;
				case 0x1:
					write_register(registers, rd, left << (right & 0x3F));
					break;
				case 0x2:
					write_register(registers, rd,
					               static_cast<int64_t>(left) < static_cast<int64_t>(right));
					break;
				case 0x3:
					write_register(registers, rd, left < right);
					break;
				case 0x4:
					write_register(registers, rd, left ^ right);
					break;
				case 0x5:
					write_register(registers, rd, left >> (right & 0x3F));
					break;
				case 0x6:
					write_register(registers, rd, left | right);
					break;
				case 0x7:
					write_register(registers, rd, left & right);
					break;
				default:
					unsupported = true;
					break;
				}
			} else if (funct7 == 0x20) {
				switch (funct3) {
				case 0x0:
					write_register(registers, rd, left - right);
					break;
				case 0x5:
					write_register(registers, rd,
					               static_cast<uint64_t>(static_cast<int64_t>(left) >> (right & 0x3F)));
					break;
				default:
					unsupported = true;
					break;
				}
			} else if (funct7 == 0x01) {
				switch (funct3) {
				case 0x0:
					write_register(registers, rd, left * right);
					break;
				case 0x1:
					write_register(registers, rd, high_signed(left, right));
					break;
				case 0x2:
					write_register(registers, rd, high_signed_unsigned(left, right));
					break;
				case 0x3:
					write_register(registers, rd, high_unsigned(left, right));
					break;
				case 0x4:
					write_register(registers, rd, divide_signed(left, right));
					break;
				case 0x5:
					write_register(registers, rd, divide_unsigned(left, right));
					break;
				case 0x6:
					write_register(registers, rd, remainder_signed(left, right));
					break;
				case 0x7:
					write_register(registers, rd, remainder_unsigned(left, right));
					break;
				default:
					unsupported = true;
					break;
				}
			} else {
				unsupported = true;
			}
			break;
		}
		case 0x3B: { // OP-32
			const uint32_t left = static_cast<uint32_t>(registers[rs1]);
			const uint32_t right = static_cast<uint32_t>(registers[rs2]);
			if (funct7 == 0x00) {
				switch (funct3) {
				case 0x0:
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(static_cast<uint32_t>(left + right), 32)));
					break;
				case 0x1:
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(left << (right & 0x1F), 32)));
					break;
				case 0x5:
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(left >> (right & 0x1F), 32)));
					break;
				default:
					unsupported = true;
					break;
				}
			} else if (funct7 == 0x20) {
				switch (funct3) {
				case 0x0:
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(static_cast<uint32_t>(left - right), 32)));
					break;
				case 0x5:
					write_register(registers, rd, static_cast<uint64_t>(sign_extend(
					                                   static_cast<uint32_t>(static_cast<int32_t>(left) >> (right & 0x1F)), 32)));
					break;
				default:
					unsupported = true;
					break;
				}
			} else if (funct7 == 0x01) {
				switch (funct3) {
				case 0x0:
					write_register(registers, rd,
					               static_cast<uint64_t>(sign_extend(left * right, 32)));
					break;
				case 0x4:
					write_register(registers, rd, static_cast<uint64_t>(
					                                   sign_extend(static_cast<uint32_t>(divide_signed_word(left, right)), 32)));
					break;
				case 0x5:
					write_register(registers, rd, static_cast<uint64_t>(
					                                   sign_extend(static_cast<uint32_t>(divide_unsigned_word(left, right)), 32)));
					break;
				case 0x6:
					write_register(registers, rd, static_cast<uint64_t>(
					                                   sign_extend(static_cast<uint32_t>(remainder_signed_word(left, right)), 32)));
					break;
				case 0x7:
					write_register(registers, rd, static_cast<uint64_t>(
					                                   sign_extend(static_cast<uint32_t>(remainder_unsigned_word(left, right)), 32)));
					break;
				default:
					unsupported = true;
					break;
				}
			} else {
				unsupported = true;
			}
			break;
		}
		case 0x0F: // FENCE
			if (funct3 != 0) {
				unsupported = true;
			}
			break;
		default:
			unsupported = true;
			break;
		}

		if (unsupported) {
			break;
		}
		retired++;
		current = target;
	}
	*pc = current;
	registers[0] = 0;
	return retired;
}
