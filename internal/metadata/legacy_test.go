package metadata_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLegacyRuntimeOnlyUsedByPOSIXGate 验证 R06:退役的 WASI
// Runtime/Session 只允许 POSIX gate 显式调用,不得回到公开入口。
func TestLegacyRuntimeOnlyUsedByPOSIXGate(t *testing.T) {
	root := "../.."
	allowed := map[string]bool{
		"test/posix/main.go":     true,
		"test/posix/upstream.go": true,
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "docs", "testdata", "var", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		source, err := os.ReadFile(path) //nolint:gosec // 仓库内只读审计
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if name == "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/legacy" && !allowed[relative] {
				t.Errorf("旧 WASI 运行时只能由 POSIX gate 显式调用: %s", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
