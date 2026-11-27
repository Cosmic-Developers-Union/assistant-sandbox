package hostroot

import (
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEmbeddedArchiveDigestAndELF(t *testing.T) {
	archive := Archive()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		if archive != nil {
			t.Fatal("non-amd64 build unexpectedly embeds the amd64 rootfs")
		}
		t.Skip("amd64 rootfs is embedded only on linux/amd64")
	}
	if err := Verify(archive); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := Extract(t.Context(), archive, root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"usr/bin/node", "usr/bin/python3.12", "lib/ld-musl-x86_64.so.1"} {
		file, err := elf.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 {
			t.Fatalf("%s class=%v machine=%v", name, file.Class, file.Machine)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"usr/bin/node", "usr/bin/python3.12"} {
		file, err := elf.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		interpreter, err := elfInterpreter(file)
		if err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if interpreter != "/lib/ld-musl-x86_64.so.1" {
			t.Fatalf("%s interpreter=%q", name, interpreter)
		}
		needed, err := file.DynString(elf.DT_NEEDED)
		if err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		for _, library := range needed {
			if !rootfsLibraryExists(root, library) {
				t.Fatalf("%s dependency %s is not in the image", name, library)
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorruptArchiveFailsBeforeExtraction(t *testing.T) {
	destination := t.TempDir()
	corrupt := []byte("not an amd64 root filesystem")
	if err := Verify(corrupt); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("Verify error=%v want ErrInvalidArchive", err)
	}
	if err := Extract(t.Context(), corrupt, destination); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("Extract error=%v want ErrInvalidArchive", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("corrupt image created %d entries", len(entries))
	}
}

func elfInterpreter(file *elf.File) (string, error) {
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		data := make([]byte, program.Filesz)
		if _, err := program.ReadAt(data, 0); err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), "\x00"), nil
	}
	return "", errors.New("PT_INTERP missing")
}

func rootfsLibraryExists(root, name string) bool {
	for _, directory := range []string{"usr/lib", "lib", "usr/local/lib"} {
		if _, err := os.Stat(filepath.Join(root, directory, name)); err == nil {
			return true
		}
	}
	return false
}
