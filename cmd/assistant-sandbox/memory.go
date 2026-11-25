package main

import (
	"fmt"
	"math/big"
	"strings"
)

// parseMemory 解析 256m、300m、1.5g、16g 等规范内存值。无后缀按
// 字节;后缀 k/m/g/t 按二进制倍数解释。
func parseMemory(value string) (uint64, error) {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		return 0, fmt.Errorf("内存值不能为空")
	}
	multiplier := uint64(1)
	for _, suffix := range []struct {
		name       string
		multiplier uint64
	}{
		{"tib", 1 << 40}, {"tb", 1 << 40}, {"t", 1 << 40},
		{"gib", 1 << 30}, {"gb", 1 << 30}, {"g", 1 << 30},
		{"mib", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20},
		{"kib", 1 << 10}, {"kb", 1 << 10}, {"k", 1 << 10},
		{"b", 1},
	} {
		if strings.HasSuffix(text, suffix.name) {
			text = strings.TrimSpace(strings.TrimSuffix(text, suffix.name))
			multiplier = suffix.multiplier
			break
		}
	}
	number, ok := new(big.Rat).SetString(text)
	if !ok || number.Sign() <= 0 {
		return 0, fmt.Errorf("非法内存值 %q", value)
	}
	bytes := new(big.Rat).Mul(number, new(big.Rat).SetInt(new(big.Int).SetUint64(multiplier)))
	rounded := new(big.Int).Quo(bytes.Num(), bytes.Denom())
	remainder := new(big.Int).Rem(bytes.Num(), bytes.Denom())
	if new(big.Int).Lsh(remainder, 1).Cmp(bytes.Denom()) >= 0 {
		rounded.Add(rounded, big.NewInt(1))
	}
	if rounded.Sign() <= 0 {
		return 0, fmt.Errorf("非法内存值 %q", value)
	}
	if rounded.BitLen() > 64 {
		return 0, fmt.Errorf("内存值 %q 超出 uint64", value)
	}
	return rounded.Uint64(), nil
}
