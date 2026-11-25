package native_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
)

func TestInstallProvidesNativeBashAndSh(t *testing.T) {
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeshell.Install(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"/bin/bash", "/bin/busybox", "/sbin/init", "/lib/ld-musl-riscv64.so.1", "/usr/lib/libreadline.so.8"} {
		if _, err := system.Stat(t.Context(), name); err != nil {
			t.Fatalf("缺少 %s: %v", name, err)
		}
	}
	for _, link := range []string{"/bin/sh", "/bin/ls"} {
		target, err := system.Readlink(t.Context(), link)
		if err != nil || target != "busybox" {
			t.Fatalf("%s -> %q, %v", link, target, err)
		}
	}
	if _, err := nativeshell.Parse("zsh"); err == nil {
		t.Fatal("zsh 不应被声明为支持")
	}
}

func TestEmbeddedAssetsMatchPinnedN10Digests(t *testing.T) {
	for name, want := range map[string]string{
		"bash":                 "009e1bab308b4c6df742c7768ae0fa7b182b78256f92f93acdb10d516bf39a94",
		"busybox.static":       "62831fb7c4a0da509481107a8aeb022244235c5dced18101e3d39131d303d704",
		"ld-musl-riscv64.so.1": "23ea7ef0c0fb58d350b0a847c75de7529a68c240af87c4fdd4a327444801b0fd",
		"libncursesw.so.6.5":   "28e544f2e21d8a9a0458b8f3425ee46a416c0d35d2ec1a4ae44fa60b9019c3e4",
		"libreadline.so.8.2":   "e8cce022079187a8b353a6baa7611f44092d97bd98096d538cc5a6f81729cca2",
	} {
		data, err := os.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("%s sha256=%s want %s", name, got, want)
		}
	}
}
