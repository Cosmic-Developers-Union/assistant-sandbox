package riscv64

import "math/big"

// fpParams 描述一种浮点格式的舍入参数。
type fpParams struct {
	width uint // 寄存器内位型总宽(32/64)
	prec  uint // 有效位宽(含隐含位)
	emin  int
	emax  int
	bias  uint
	qnan  uint64 // 规范安静 NaN 位型
}

// expBits 返回指数字段位宽;expShift 返回其最低位位置;signShift 返回符号位位置。
func (p fpParams) expBits() uint { return p.width - p.prec }

func (p fpParams) expShift() uint { return p.prec - 1 }

func (p fpParams) signShift() uint { return p.width - 1 }

func (p fpParams) mantMask() uint64 { return uint64(1)<<(p.prec-1) - 1 }

// expMask 返回指数字段的取值掩码;maxExp 返回全 1 指数(NaN/Inf 编码)。
func (p fpParams) expMask() int { return (1 << p.expBits()) - 1 }

var (
	fpS = fpParams{width: 32, prec: 24, emin: -126, emax: 127, bias: 127, qnan: 0x7FC00000}
	fpD = fpParams{width: 64, prec: 53, emin: -1023, emax: 1023, bias: 1023, qnan: 0x7FF8000000000000}
)

// fpMode 对应 RISC-V 的 frm 编码。
type fpMode uint8

const (
	modeRNE fpMode = 0
	modeRTZ fpMode = 1
	modeRDN fpMode = 2
	modeRUP fpMode = 3
	modeRMM fpMode = 4
)

// fflags 位(RISC-V 规范顺序)。
const (
	flagNX uint32 = 1 << 0
	flagUF uint32 = 1 << 1
	flagOF uint32 = 1 << 2
	flagDZ uint32 = 1 << 3
	flagNV uint32 = 1 << 4
)

// fpClass 是 IEEE 754 的值分类。
type fpClass uint8

const (
	classNaN fpClass = iota
	classInf
	classZero
	classFinite
)

// fpValue 是展开后的操作数:NaN/Inf/零直接分类,非零有限值保留精确有理数。
type fpValue struct {
	neg   bool
	class fpClass
	rat   *big.Rat
	snan  bool
}

// decodeFP 按位型展开一个浮点值;prec/bias 决定规格化边界。
func decodeFP(bits uint64, p fpParams) fpValue {
	neg := bits>>p.signShift()&1 != 0
	mant := bits & p.mantMask()
	rawExp := int(bits>>p.expShift()) & p.expMask()
	switch {
	case rawExp == p.expMask():
		if mant == 0 {
			return fpValue{neg: neg, class: classInf}
		}
		return fpValue{neg: neg, class: classNaN, snan: mant>>(p.prec-2)&1 == 0}
	case rawExp == 0:
		if mant == 0 {
			return fpValue{neg: neg, class: classZero}
		}
		return fpValue{neg: neg, class: classFinite,
			rat: new(big.Rat).SetFrac(new(big.Int).SetUint64(mant),
				new(big.Int).Lsh(big.NewInt(1), uint(int(p.prec)-1-p.emin)))}
	default:
		value := new(big.Int).SetUint64(mant | 1<<(p.prec-1))
		shift := rawExp - int(p.bias) - int(p.prec) + 1
		if shift >= 0 {
			value.Lsh(value, uint(shift))
			return fpValue{neg: neg, class: classFinite, rat: new(big.Rat).SetInt(value)}
		}
		return fpValue{neg: neg, class: classFinite,
			rat: new(big.Rat).SetFrac(value, new(big.Int).Lsh(big.NewInt(1), uint(-shift)))}
	}
}

// nanFlags 合并 NaN 输入的处置:安静传播,sNaN 触发 NV。
func nanResult(values ...fpValue) (uint64, uint32) {
	var flags uint32
	for _, v := range values {
		if v.class == classNaN && v.snan {
			flags |= flagNV
		}
	}
	return 0, flags // 位型由调用方按格式补 qNaN。
}

// log2Floor 返回 floor(log2(n/d)),要求 n、d > 0。
func log2Floor(n, d *big.Int) int {
	e := n.BitLen() - d.BitLen()
	if e >= 0 {
		if n.Cmp(new(big.Int).Lsh(d, uint(e))) < 0 {
			e--
		} else if n.Cmp(new(big.Int).Lsh(d, uint(e+1))) >= 0 {
			e++
		}
	} else {
		shifted := new(big.Int).Lsh(n, uint(-e))
		switch shifted.Cmp(d) {
		case -1:
			e--
		case 0:
		default:
			if new(big.Int).Lsh(shifted, 1).Cmp(d) >= 0 {
				e++
			}
		}
	}
	return e
}

// roundMantissa 按 mode 对 q + rem/d 做幅值方向舍入。
func roundMantissa(q, rem, d *big.Int, mode fpMode) {
	if rem.Sign() == 0 {
		return
	}
	twice := new(big.Int).Lsh(rem, 1)
	switch mode {
	case modeRNE:
		switch twice.Cmp(d) {
		case 1:
			q.Add(q, big.NewInt(1))
		case 0:
			if q.Bit(0) == 1 {
				q.Add(q, big.NewInt(1))
			}
		}
	case modeRMM:
		if twice.Cmp(d) >= 0 {
			q.Add(q, big.NewInt(1))
		}
	case modeRUP:
		q.Add(q, big.NewInt(1))
	}
}

// roundPositive 把精确正有理数舍入为规格化结果:
// 返回 (mant, exp, inexact, tiny, overflow),真值 = mant × 2^(exp-prec+1)。
func roundPositive(r *big.Rat, p fpParams, mode fpMode) (uint64, int, bool, bool, bool) {
	num, den := r.Num(), r.Denom()
	if num.Sign() == 0 {
		return 0, 0, false, false, false
	}
	e := log2Floor(num, den)
	// 以 prec 位定点表示:值 = q / 2^(prec-1-e)。
	s := int(p.prec) - 1 - e
	scaled := new(big.Int).Lsh(num, uint(max(s, 0)))
	divisor := new(big.Int).Lsh(den, uint(max(-s, 0)))
	q := new(big.Int).Quo(scaled, divisor)
	rem := new(big.Int).Sub(scaled, new(big.Int).Mul(q, divisor))
	inexact := rem.Sign() != 0

	switch mode {
	case modeRNE:
		roundMantissa(q, rem, divisor, modeRNE)
	case modeRMM:
		roundMantissa(q, rem, divisor, modeRMM)
	case modeRUP:
		roundMantissa(q, rem, divisor, modeRUP)
	}
	if q.BitLen() > int(p.prec) {
		q.Rsh(q, 1)
		e++
		inexact = true
	}

	tiny := e < p.emin
	if e > p.emax {
		return 0, e, true, false, true
	}
	if tiny {
		drop := uint(p.emin - e)
		low := new(big.Int).And(q, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), drop), big.NewInt(1)))
		q.Rsh(q, drop)
		if low.Sign() != 0 {
			inexact = true
		}
		roundMantissa(q, low, new(big.Int).Lsh(big.NewInt(1), drop), mode)
		e = p.emin
		if q.Cmp(new(big.Int).Lsh(big.NewInt(1), p.prec-1)) >= 0 {
			// 次正规舍入进位为最小规格数。
			e++
			tiny = false
		}
	}
	return q.Uint64(), e, inexact, tiny, false
}

// packFP 把符号 + 舍入结果编码为寄存器位型,并输出异常标志。
func packFP(neg bool, mant uint64, exp int, inexact, tiny, overflow bool, p fpParams, mode fpMode) (uint64, uint32) {
	var flags uint32
	if overflow {
		flags |= flagOF | flagNX
		switch mode {
		case modeRNE:
			return encodeInf(neg, p), flags
		case modeRTZ:
			return encodeMaxFinite(neg, p), flags
		case modeRDN:
			if neg {
				return encodeInf(true, p), flags
			}
			return encodeMaxFinite(false, p), flags
		case modeRUP:
			if neg {
				return encodeMaxFinite(true, p), flags
			}
			return encodeInf(false, p), flags
		default:
			return encodeInf(neg, p), flags
		}
	}
	if inexact {
		flags |= flagNX
	}
	if tiny {
		flags |= flagUF
	}
	var bits uint64
	if mant == 0 {
		return negBits(neg, p), flags
	}
	if exp == p.emin && mant < uint64(1)<<(p.prec-1) {
		bits = mant // 次正规数:指数字段为 0。
	} else {
		bits = uint64(exp+int(p.bias))<<p.expShift() | mant&p.mantMask()
	}
	if neg {
		bits |= 1 << p.signShift()
	}
	return bits, flags
}

// negBits 返回带符号的零位型。
func negBits(neg bool, p fpParams) uint64 {
	if neg {
		return 1 << p.signShift()
	}
	return 0
}

func encodeInf(neg bool, p fpParams) uint64 {
	bits := uint64(p.expMask()) << p.expShift()
	if neg {
		bits |= 1 << p.signShift()
	}
	return bits
}

func encodeMaxFinite(neg bool, p fpParams) uint64 {
	bits := uint64(p.expMask()-1)<<p.expShift() | p.mantMask()
	if neg {
		bits |= 1 << p.signShift()
	}
	return bits
}

// fpRound 对精确有理数(可带符号)做一次舍入并返回位型与标志。
// neg 为强制符号:exact 为零时按它决定 ±0,非零时真值符号由 exact 决定。
func fpRound(exact *big.Rat, p fpParams, mode fpMode, forcedNeg *bool) (uint64, uint32) {
	sign := exact.Sign()
	neg := sign < 0
	if sign == 0 {
		if forcedNeg != nil {
			neg = *forcedNeg
		} else if mode == modeRDN {
			// 向 -∞ 舍入的精确零结果为 -0(IEEE 加减规则)。
			neg = true
		}
		return negBits(neg, p), 0
	}
	if neg {
		exact = new(big.Rat).Neg(exact)
	}
	mode = effectiveMode(mode, neg)
	mant, exp, inexact, tiny, overflow := roundPositive(exact, p, mode)
	return packFP(neg, mant, exp, inexact, tiny, overflow, p, mode)
}

// effectiveMode 按符号翻转方向性舍入:对负值,"向 -∞"等价于对幅值"向 +∞"。
func effectiveMode(mode fpMode, neg bool) fpMode {
	if !neg {
		return mode
	}
	switch mode {
	case modeRDN:
		return modeRUP
	case modeRUP:
		return modeRDN
	default:
		return mode
	}
}

// fpSqrt 对精确正有理数求平方根:整数平方根给出 floor 形式的定点近似,
// 余数参与舍入,保证结果与标志精确。
func fpSqrt(r *big.Rat, p fpParams, mode fpMode) (uint64, uint32) {
	// 挑选缩放 k 使 sqrt(r) ∈ [2^(prec-1), 2^prec):r = n/d,
	// sqrt(r) = sqrt(n·d·2^(2k))/d·2^k?? 直接用 sqrt(n·2^(2k)/d) = sqrt(n·2^(2k))/sqrt(d)
	// 不整除;改走 sqrt(n·d) 路径:sqrt(n/d) = sqrt(n·d)/d。
	num := new(big.Int).Mul(r.Num(), r.Denom())
	den := r.Denom()
	// sqrt(num) ≥ 2^(prec-1)·den 所需缩放:补 2^shift 使位长足够。
	target := uint(2)*(p.prec-1) + 2*uint(den.BitLen()) + 8
	shift := 0
	if t := int(target); num.BitLen() < t {
		shift = t - num.BitLen()
		if shift%2 == 1 {
			shift++
		}
	}
	scaled := new(big.Int).Lsh(num, uint(shift))
	root := new(big.Int).Sqrt(scaled)
	// 精确值 = root / (den · 2^(shift/2));化成定点再舍入。
	value := new(big.Rat).SetFrac(root, new(big.Int).Lsh(den, uint(shift/2)))
	neg := false
	return fpRound(value, p, mode, &neg)
}
