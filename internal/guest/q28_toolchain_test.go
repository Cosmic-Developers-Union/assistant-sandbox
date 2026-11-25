package guest_test

import (
	"bytes"
	"context"
	"debug/elf"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

const (
	tccToolchainDigest    = "dc16d9d4e2b8f3bd1301cf20519efc35fd44ab3ee7a05db3988c16d3bd73ce04"
	gccCXXToolchainDigest = "a9a452dc848090af9661f8af25f9d541683edb7d665cf4cb1a55f958623199d5"
)

// guestToolchainGuest 装配真实 riscv64 客体工具链。TinyCC 提供完整
// C 编译/链接;可选 GCC cc1plus + GNU as/ld 提供 C++ 前端与链接步骤。
func guestToolchainGuest(t *testing.T, withCXX bool) (*instance.VM, *instance.Connection) {
	t.Helper()
	machine, err := instance.New(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		Session:       resources.Limits{Memory: 4 << 30, Work: 1 << 38},
		Process:       resources.Limits{Memory: 2 << 30, Work: 1 << 38},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	installN10(t, machine)
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/tcc-riscv64-toolchain.tar.gz", tccToolchainDigest))
	if withCXX {
		extractGuestTar(t, machine, python312Asset(t,
			"testdata/gcc14-cxx-riscv64-toolchain.tar.gz", gccCXXToolchainDigest))
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return machine, connection
}

// TestGuestNativeToolchainCompilesC 验证 Q28:客体 TinyCC、musl 头文件与
// 链接器在 VM 内编译、链接并运行最小 C 程序,不借用宿主编译器产物。
func TestGuestNativeToolchainCompilesC(t *testing.T) {
	machine, connection := guestToolchainGuest(t, false)
	source := `#include <stdio.h>
#include <stdlib.h>
#include <stddef.h>

static int square(int value) { return value * value; }

void *memmove(void *destination, const void *source, size_t count) {
	unsigned char *to = destination;
	const unsigned char *from = source;
	if (to < from) {
		for (size_t i = 0; i < count; i++) to[i] = from[i];
	} else {
		for (size_t i = count; i > 0; i--) to[i - 1] = from[i - 1];
	}
	return destination;
}

int main(void) {
	int total = 0;
	for (int i = 1; i <= 6; i++) total += square(i);
	if (total != 91) return 2;
	printf("Q28-C %d %s\n", total, getenv("Q28_GUEST"));
	return 0;
}
`
	writeGuestFile(t, machine, "/work/q28.c", []byte(source), 0o644)
	compileOutput, compileErr, compileCode := runGuestProgram(t, connection,
		"/usr/bin/tcc", []string{"-c", "-O2", "-o", "/work/q28.o", "/work/q28.c"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 600*time.Second)
	if compileCode != 0 {
		t.Fatalf("客体 tcc 编译 exit=%d stdout=%q stderr=%q", compileCode, compileOutput, compileErr)
	}
	linkOutput, linkErr, linkCode := runGuestProgram(t, connection,
		"/usr/bin/tcc", []string{"-static", "-O2", "-o", "/work/q28-c", "/work/q28.o", "-lc"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 600*time.Second)
	if linkCode != 0 {
		t.Fatalf("客体 tcc 链接 exit=%d stdout=%q stderr=%q", linkCode, linkOutput, linkErr)
	}
	output, diagnostic, code := runGuestProgram(t, connection,
		"/work/q28-c", nil,
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work", "Q28_GUEST": "inside"}, 120*time.Second)
	if code != 0 || output != "Q28-C 91 inside\n" {
		t.Fatalf("客体产物 exit=%d stdout=%q stderr=%q (tcc stdout=%q tcc stderr=%q)",
			code, output, diagnostic, compileOutput+linkOutput, compileErr+linkErr)
	}
	assertGuestRISCV64ELF(t, machine, "/work/q28-c")
}

// TestGuestNativeToolchainCompilesCXX 验证 Q28 的 C++ 路径:GCC 14.2
// cc1plus 在客体生成汇编,GNU as/ld 在客体完成目标文件与静态 ELF 链接。
func TestGuestNativeToolchainCompilesCXX(t *testing.T) {
	machine, connection := guestToolchainGuest(t, true)
	source := `extern "C" int puts(const char *);
class Adder {
public:
	explicit Adder(int base) : base_(base) {}
	int add(int value) const { return base_ + value; }
private:
	int base_;
};
int main() {
	Adder adder(20);
	char output[] = "Q28-CXX 00";
	output[8] = '0' + adder.add(22) / 10;
	output[9] = '0' + adder.add(22) % 10;
	return puts(output) < 0;
}
`
	writeGuestFile(t, machine, "/work/q28.cc", []byte(source), 0o644)
	compiled, compileErr, compileCode := runGuestProgram(t, connection,
		"/usr/libexec/gcc/riscv64-alpine-linux-musl/14.2.0/cc1plus",
		[]string{"-quiet", "-D_GNU_SOURCE", "/work/q28.cc", "-quiet",
			"-dumpdir", "/work/q28-cxx-", "-dumpbase", "q28.cc", "-dumpbase-ext", ".cc",
			"-march=rv64gc", "-mabi=lp64d", "-misa-spec=20191213",
			"-mtls-dialect=trad", "-march=rv64imafdc_zicsr_zifencei", "-O2",
			"-o", "/work/q28.s"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 600*time.Second)
	if compileCode != 0 {
		t.Fatalf("cc1plus exit=%d stdout=%q stderr=%q", compileCode, compiled, compileErr)
	}
	assembled, assembledErr, assembledCode := runGuestProgram(t, connection,
		"/usr/riscv64-alpine-linux-musl/bin/as",
		[]string{"--traditional-format", "-fpic", "-march=rv64gc",
			"-march=rv64imafdc_zicsr_zifencei", "-mabi=lp64d", "-misa-spec=20191213",
			"-o", "/work/q28.o", "/work/q28.s"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 120*time.Second)
	if assembledCode != 0 {
		t.Fatalf("as exit=%d stdout=%q stderr=%q", assembledCode, assembled, assembledErr)
	}
	linked, linkedErr, linkedCode := runGuestProgram(t, connection,
		"/usr/riscv64-alpine-linux-musl/bin/ld",
		[]string{"-melf64lriscv", "-static", "-o", "/work/q28-cxx",
			"/usr/lib/crt1.o", "/usr/lib/crti.o",
			"/usr/lib/gcc/riscv64-alpine-linux-musl/14.2.0/crtbeginT.o",
			"/work/q28.o", "--start-group",
			"/usr/lib/gcc/riscv64-alpine-linux-musl/14.2.0/libgcc.a",
			"/usr/lib/gcc/riscv64-alpine-linux-musl/14.2.0/libgcc_eh.a",
			"/usr/lib/libc.a", "--end-group",
			"/usr/lib/gcc/riscv64-alpine-linux-musl/14.2.0/crtend.o",
			"/usr/lib/crtn.o"},
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 120*time.Second)
	if linkedCode != 0 {
		t.Fatalf("ld exit=%d stdout=%q stderr=%q", linkedCode, linked, linkedErr)
	}
	output, diagnostic, code := runGuestProgram(t, connection, "/work/q28-cxx", nil,
		map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/work"}, 120*time.Second)
	if code != 0 || output != "Q28-CXX 42\n" {
		t.Fatalf("C++ 客体产物 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	assertGuestRISCV64ELF(t, machine, "/work/q28-cxx")
}

func assertGuestRISCV64ELF(t *testing.T, machine *instance.VM, name string) {
	t.Helper()
	compiled, err := readGuestFile(machine, name)
	if err != nil {
		t.Fatal(err)
	}
	image, err := elf.NewFile(bytes.NewReader([]byte(compiled)))
	if err != nil {
		t.Fatalf("客体产物 %s 不是 ELF: %v", name, err)
	}
	defer func() { _ = image.Close() }()
	if image.Machine != elf.EM_RISCV || image.Class != elf.ELFCLASS64 {
		t.Fatalf("客体产物 %s 架构=%s/%s want RISC-V/ELF64", name, image.Machine, image.Class)
	}
}

func runGuestProgram(
	t *testing.T,
	connection *instance.Connection,
	program string,
	args []string,
	env map[string]string,
	timeout time.Duration,
) (string, string, uint8) {
	t.Helper()
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	var stdout, stderr io.Writer = output, diagnostic
	if os.Getenv("PYTHON_SUITE_STREAM") != "" {
		stdout = io.MultiWriter(output, os.Stderr)
		stderr = io.MultiWriter(diagnostic, os.Stderr)
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: program,
		Args:    args,
		Dir:     "/work",
		Env:     env,
		Streams: fd.Streams{Output: stdout, Error: stderr},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatalf("%s: %v (stdout=%q stderr=%q)", program, err, output.String(), diagnostic.String())
	}
	return output.String(), diagnostic.String(), state.ExitCode
}
