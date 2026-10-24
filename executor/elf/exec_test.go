package elf

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"

	loaderelf "github.com/Cosmic-Developers-Union/assistant-sandbox/loader/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// buildExecCallerImage 构造带自定义数据段布局的 ELF:blobs 以数据段
// 内偏移放置(供 execve 的路径/argv/envp 指针引用)。
func buildExecCallerImage(t *testing.T, program []uint32, blobs map[uint64][]byte) []byte {
	t.Helper()
	raw := make([]byte, imageDataOffset+0x100)
	copy(raw[0:4], "\x7fELF")
	raw[4] = 2
	raw[5] = 1
	raw[6] = 1
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], imageEntry)
	binary.LittleEndian.PutUint64(raw[32:], imagePhdrOffset)
	binary.LittleEndian.PutUint16(raw[52:], imageHeaderSize)
	binary.LittleEndian.PutUint16(raw[54:], imagePhdrSize)
	binary.LittleEndian.PutUint16(raw[56:], 2)
	putProgramHeader(raw[imagePhdrOffset:], 1, 5, 0, imageCodeBase, imageCodeSize, imageCodeSize, 0x1000)
	putProgramHeader(raw[imagePhdrOffset+imagePhdrSize:], 1, 6, imageDataOffset, imageDataBase, 0x100, 0x100, 0x1000)
	for offset, blob := range blobs {
		copy(raw[imageDataOffset+offset:], blob)
	}
	for index, instruction := range program {
		binary.LittleEndian.PutUint32(raw[imageProgramAt+index*4:], instruction)
	}
	return raw
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// writeGuestFile 向测试文件系统写入客体文件。
func writeGuestFile(t *testing.T, system vmfs.System, path string, content []byte) {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.Create|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
}

// execControlStub 记录 execve 成功后的内核侧复位调用(H07)。
type execControlStub struct {
	*sigControlStub
	calls []ResetCall
}

type ResetCall struct {
	Path string
	Args []string
	Env  map[string]string
}

func (s *execControlStub) ResetForExec(path string, args []string, env map[string]string) error {
	s.calls = append(s.calls, ResetCall{Path: path, Args: args, Env: env})
	return nil
}

// Image2 读取初始栈:argv[0][0] 与 argv[1][0] 之差加 0x30 作为退出码,
// 同时证明映像替换与参数顺序(H06)。
var image2Program = []uint32{
	0x00013283, // ld t0, 0(sp)     → argc
	0x00813303, // ld t1, 8(sp)     → argv[0](rd=t1=6)
	0x01013383, // ld t2, 16(sp)    → argv[1]
	0x00034E03, // lbu t3, 0(t1)
	0x0003CE83, // lbu t4, 0(t2)(rs1=t2=7)
	0x41DE0533, // sub a0, t3, t4
	0x03050513, // addi a0, a0, 0x30
	0x05D00893, // li a7, 93 (exit)
	0x00000073, // ecall
}

// TestRealELFExecveReplacesImage 用真实 RISC-V ELF 验证 execve 全链路:
// 参数顺序正确、PID 内执行流替换、内核侧复位被调用(H06/H07)。
func TestRealELFExecveReplacesImage(t *testing.T) {
	// execve("/second", ["second","one"], ["K=V"])
	caller := []uint32{
		0x000125B7, // lui a1, 0x12
		0x04058593, // addi a1, a1, 64  → argv 数组 @0x12040
		0x00012637, // lui a2, 0x12
		0x06060613, // addi a2, a2, 96  → envp 数组 @0x12060
		0x00012537, // lui a0, 0x12     → path @0x12000
		0x0DD00893, // li a7, 221 (execve)
		0x00000073, // ecall
		0x05D00893, // li a7, 93 (exit;成功不返回,失败以 errno 退出)
		0x00000073, // ecall
	}
	// argv @0x12040 = [0x12010, 0x12020, NULL];envp @0x12060 = [0x12030, NULL]。
	pointer := func(address uint64) []byte {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], address)
		return raw[:]
	}
	image := buildExecCallerImage(t, caller, map[uint64][]byte{
		0x00: []byte("/second\x00"),
		0x10: []byte("second\x00"),
		0x20: []byte("one\x00"),
		0x30: []byte("K=V\x00"),
		0x40: concat(pointer(0x12010), pointer(0x12020), pointer(0)),
		0x60: concat(pointer(0x12030), pointer(0)),
	})
	harness := newHarness(t, image, nil)
	writeGuestFile(t, harness.system, "/second", buildImageWith(t, image2Program, ""))
	stub := &execControlStub{sigControlStub: &sigControlStub{actions: map[signal.Number]signal.ActionConfig{}}}
	harness.process.Control = stub

	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	// argv[0][0]='s'(0x73),argv[1][0]='o'(0x6F):差 + 0x30 = 0x34;
	// 顺序颠倒则为 0x2C,未替换则退出码为 0x42 路径之外。
	if code != 0x34 {
		t.Fatalf("退出码=%#x want 0x34(映像替换且参数顺序正确)", code)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("内核复位调用=%d want 1", len(stub.calls))
	}
	call := stub.calls[0]
	if call.Path != "/second" || len(call.Args) != 2 || call.Args[0] != "second" || call.Args[1] != "one" {
		t.Fatalf("复位参数=%+v", call)
	}
	if call.Env["K"] != "V" {
		t.Fatalf("复位环境=%+v", call.Env)
	}
}

// TestRealELFExecveShebang 用真实 RISC-V ELF 验证 shebang 解析:
// "#!/second --flag" 脚本经解释器装载并携带可选参数,
// argv = ["/second", "--flag", "/script"]
// (argv[0][0]='/'=0x2F,argv[1][0]='-'=0x2D,差 + 0x30 = 0x32)。
func TestRealELFExecveShebang(t *testing.T) {
	caller := []uint32{
		0x000125B7, // lui a1, 0x12
		0x04058593, // addi a1, a1, 64  → argv 数组 @0x12040
		0x00012637, // lui a2, 0x12
		0x06060613, // addi a2, a2, 96  → envp 数组 @0x12060
		0x00012537, // lui a0, 0x12     → 脚本路径 @0x12000
		0x0DD00893, // li a7, 221 (execve)
		0x00000073, // ecall
		0x05D00893, // li a7, 93
		0x00000073, // ecall
	}
	pointer := func(address uint64) []byte {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], address)
		return raw[:]
	}
	image := buildExecCallerImage(t, caller, map[uint64][]byte{
		0x00: []byte("/script\x00"),
		0x10: []byte("/script\x00"),
		0x40: concat(pointer(0x12010), pointer(0)),
		0x60: concat(pointer(0)),
	})
	harness := newHarness(t, image, nil)
	writeGuestFile(t, harness.system, "/second", buildImageWith(t, image2Program, ""))
	writeGuestFile(t, harness.system, "/script", []byte("#!/second --flag\n"))

	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if code != 0x32 {
		t.Fatalf("退出码=%#x want 0x32(shebang 解释器与可选参数的 argv 顺序)", code)
	}
}

// TestRealELFExecveFailureKeepsOldProgram 验证 H07 原子性:装载失败
// (ENOENT)时旧程序原样继续——以 execve 返回的 errno 低字节退出,
// 证明失败未破坏当前执行流。
func TestRealELFExecveFailureKeepsOldProgram(t *testing.T) {
	caller := []uint32{
		0x000125B7, // lui a1, 0x12
		0x04058593, // addi a1, a1, 64  → argv 数组 @0x12040
		0x00012637, // lui a2, 0x12
		0x06060613, // addi a2, a2, 96  → envp 数组 @0x12060
		0x00012537, // lui a0, 0x12     → path @0x12000
		0x0DD00893, // li a7, 221 (execve)
		0x00000073, // ecall            → a0 = -ENOENT(失败,旧程序继续)
		0x0FF57513, // andi a0, a0, 0xff(funct3=ANDI,rs1=rd=a0)
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
	}
	pointer := func(address uint64) []byte {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], address)
		return raw[:]
	}
	image := buildExecCallerImage(t, caller, map[uint64][]byte{
		0x00: []byte("/missing\x00"),
		0x40: concat(pointer(0)),
		0x60: concat(pointer(0)),
	})
	harness := newHarness(t, image, nil)
	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	// ENOENT = 2:-2 的低字节 = 0xFE = 254;程序继续执行到了退出。
	if code != 254 {
		t.Fatalf("退出码=%d want 254(ENOENT 且旧程序继续)", code)
	}
}

// TestRealELFStackOverflowHitsGuardPage 验证 E13:向栈底方向持续
// 写入最终触达保护页——进程以 SIGSEGV 终止且故障地址落在保护页内
// (SEGV/ACCERR,与未映射的 MAPERR 可区分),不会写入相邻映射。
func TestRealELFStackOverflowHitsGuardPage(t *testing.T) {
	// 循环:sp -= 24;*(sp) = x0;jal 回循环首。
	overflow := []uint32{
		0xFE810113, // addi sp, sp, -24
		0x00013023, // sd x0, 0(sp)
		0xff9ff06f, // jal x0, -8(回到 addi)
	}
	harness := newHarness(t, buildImageWith(t, overflow, ""), nil)
	// 信号控制端口存在时,J06 把保护错误转换为 SIGSEGV 投递。
	stub := &sigControlStub{actions: map[signal.Number]signal.ActionConfig{}}
	harness.process.Control = stub
	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := guest.Run(context.Background(), harness.process)
	var death executable.Death
	if !errors.As(runErr, &death) {
		t.Fatalf("err=%v want executable.Death", runErr)
	}
	if death.Signal != signal.SEGV {
		t.Fatalf("signal=%v want SIGSEGV", death.Signal)
	}
	// 保护页区间:栈区低端之下的一页(默认栈参数见 loader/elf)。
	regionStart := loaderelf.DefaultStackTop - uint64(loaderelf.DefaultStackSize)
	address := parseSiAddr(t, death.Detail)
	if address < regionStart-stackGuardBytes || address >= regionStart {
		t.Fatalf("故障地址=%#x 应落在保护页 [%#x,%#x): %q",
			address, regionStart-stackGuardBytes, regionStart, death.Detail)
	}
	// 保护错误(映射存在但无权限,SEGV_ACCERR=2),而非未映射(MAPERR=1)。
	if !strings.Contains(death.Detail, "si_code=2") {
		t.Fatalf("Detail=%q want ACCERR(si_code=2)", death.Detail)
	}
}

// parseSiAddr 从 Death.Detail 提取 si_addr=0x… 十六进制地址。
func parseSiAddr(t *testing.T, detail string) uint64 {
	t.Helper()
	const marker = "si_addr=0x"
	index := strings.Index(detail, marker)
	if index < 0 {
		t.Fatalf("Detail=%q 缺少 si_addr", detail)
	}
	raw := detail[index+len(marker):]
	end := strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > 'f' || (r > '9' && r < 'a') })
	if end > 0 {
		raw = raw[:end]
	}
	address, err := strconv.ParseUint(raw, 16, 64)
	if err != nil {
		t.Fatalf("si_addr 解析失败: %v", err)
	}
	return address
}
