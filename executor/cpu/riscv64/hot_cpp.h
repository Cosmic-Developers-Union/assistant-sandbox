//go:build cgo && linux

#ifndef ASSISTANT_SANDBOX_RISCV64_HOT_CPP_H
#define ASSISTANT_SANDBOX_RISCV64_HOT_CPP_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

uint64_t rv64_hot_run(uint64_t *registers, uint64_t *pc, uint64_t page_base,
                      uint32_t max_steps,
                      const uint64_t *regions, uint32_t region_count,
                      const uint64_t *page_numbers, const uint64_t *page_addresses,
                      uint32_t page_count, uint64_t page_size,
                      const uint64_t *code_bases, const uint64_t *code_words,
                      const uint64_t *code_lengths, uint32_t code_page_count,
                      uint64_t instruction_page_size);

#ifdef __cplusplus
}
#endif

#endif
