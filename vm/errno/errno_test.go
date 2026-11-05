package errno

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestStableGuestErrors(t *testing.T) {
	for _, code := range Codes() {
		if code.Error() == "" || !strings.Contains(code.Error(), "：") || !errors.Is(fmt.Errorf("调用: %w", code), code) {
			t.Fatal(code)
		}
	}
}

func TestWASIMappingIsStable(t *testing.T) {

	if _, version := WASI(Invalid); version != "abi-wasi-1" {
		t.Fatalf("unexpected ABI version %q", version)
	}
	cases := map[Code]uint16{
		Invalid: 28, NoProcess: 71, NoChild: 12, Busy: 10, Limit: 6, NotFound: 44,
		NotImplemented: 52, NotSupported: 58, NetworkUnreachable: 40, Permission: 2, NotPermitted: 63,
		Exist: 20, NotDirectory: 54, IsDirectory: 31, NotEmpty: 55, BadHandle: 8,
		NameTooLong: 37, ReadOnlyFS: 69, LinkLoop: 32, NoSpace: 51, NoMemory: 48,
		Cancelled: 11, Interrupted: 27, WouldBlock: 6, Range: 68, Overflow: 61,
		BrokenPipe: 64, NoExec: 45, TooBig: 57, Seek: 70, TimedOut: 73, Version: 75,
		Fault: 21, IO: 29, CrossDevice: 18, NoDevice: 60,
		AddressInUse: 3, AddressNotAvail: 4, IsConn: 30, NotConn: 53, NotSock: 57,
		ConnRefused: 14, ConnReset: 15, ConnAborted: 13, Already: 7, InProgress: 26,
		MessageSize: 35, HostUnreachable: 23,
	}

	// 每个声明码都有独立于回退值的映射,不得伪装成 ENOSYS。
	for _, code := range Codes() {
		if got, _ := WASI(code); got == 52 && code != NotImplemented {
			t.Fatalf("%s 缺少 WASI 数值映射(回退到 ENOSYS)", code)
		}
	}
	for code, want := range cases {
		if got, _ := WASI(code); got != want {
			t.Fatalf("%s: got %d want %d", code, got, want)
		}
	}

	if got, _ := WASI(Code("EUNKNOWN")); got != 52 {
		t.Fatalf("unknown code mapped to %d", got)
	}
}

func TestWrapKeepsIdentity(t *testing.T) {
	err := Wrap(Permission, "打开客体文件")
	if !errors.Is(err, Permission) || !strings.Contains(err.Error(), "打开客体文件") {
		t.Fatal(err)
	}
}

func TestLinuxMappingIsStable(t *testing.T) {

	if _, version := Linux(Invalid); version != "abi-linux-asm-generic" {
		t.Fatalf("unexpected ABI version %q", version)
	}
	cases := map[Code]int32{
		Permission: 13, NotPermitted: 1, NotFound: 2, NoProcess: 3, Interrupted: 4, NoDevice: 6,
		NoExec: 8, BadHandle: 9, NoChild: 10, Limit: 11, WouldBlock: 11, Busy: 16,
		Exist: 17, CrossDevice: 18, NotDirectory: 20, IsDirectory: 21, Invalid: 22,
		NoSpace: 28, NoMemory: 12, TooBig: 7, ReadOnlyFS: 30, BrokenPipe: 32, Range: 34, NameTooLong: 36,
		NotImplemented: 38, NotEmpty: 39, LinkLoop: 40, Version: 71, Overflow: 75,
		NotSupported: 95, NetworkUnreachable: 101, TimedOut: 110, Cancelled: 125,
		Fault: 14, IO: 5, Seek: 29,
		AddressInUse: 98, AddressNotAvail: 99, IsConn: 106, NotConn: 107, NotSock: 88,
		ConnRefused: 111, ConnReset: 104, ConnAborted: 103, Already: 114, InProgress: 115,
		MessageSize: 90, HostUnreachable: 113,
	}

	for _, code := range Codes() {
		if _, ok := cases[code]; !ok {
			t.Fatalf("%s 缺少 Linux 数值映射", code)
		}
	}
	for code, want := range cases {
		if got, _ := Linux(code); got != want {
			t.Fatalf("%s: got %d want %d", code, got, want)
		}
	}

	if got, _ := Linux(Code("EUNKNOWN")); got != 38 {
		t.Fatalf("unknown code mapped to %d", got)
	}
}
