package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tetratelabs/wazero"
)

func openCompilationCache(dir string) (wazero.CompilationCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 WASM 编译缓存: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("检查 WASM 编译缓存: %w", err)
	}

	if !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nil, fmt.Errorf("WASM 编译缓存必须是私有目录（权限 0700），不能使用符号链接")
	}
	cache, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		return nil, fmt.Errorf("打开 WASM 编译缓存: %w", err)
	}
	return cache, nil
}

func validateCacheOutsideWorkspace(dir, cacheDir string) error {
	if cacheDir == "" {
		return nil
	}
	workspace, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("定位缓存隔离工作区: %w", err)
	}
	cache, err := filepath.EvalSymlinks(cacheDir)
	if err != nil {
		return fmt.Errorf("定位编译缓存: %w", err)
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return err
	}
	cache, err = filepath.Abs(cache)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.VolumeName(workspace), filepath.VolumeName(cache)) {
		return nil
	}
	relative, err := filepath.Rel(workspace, cache)
	if err != nil {
		return fmt.Errorf("检查编译缓存隔离: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("编译缓存不能位于授权工作区内；请将缓存移到工作区外，或使用 --sandbox-cache-dir none")
	}
	return nil
}
