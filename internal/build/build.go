package build

import (
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Generate(dir, outputDir string) (resultErr error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "assistant-wasm-build-*")
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, os.RemoveAll(temporary))
	}()
	module, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(filepath.Join(dir, "go.sum"))
	if err != nil {
		return err
	}
	replacement := map[string]string{}
	for _, dependency := range []struct {
		name, directory string
		files           map[string]string
	}{
		{"mvdan.cc/sh/v3", "sh", map[string]string{"interp/stdin_os.go": "stdin_os.go.txt"}},
	} {
		sourceDir, err := dependencySource(dir, dependency.name)
		if err != nil {
			return err
		}
		root := filepath.Join(temporary, dependency.directory)
		if err := os.CopyFS(root, os.DirFS(sourceDir)); err != nil {
			return fmt.Errorf("复制依赖 %s: %w", dependency.name, err)
		}
		if dependency.name == "mvdan.cc/sh/v3" {
			if err := adaptInterpreter(root); err != nil {
				return err
			}
		}
		module = append(module, []byte("\nreplace "+dependency.name+" => "+filepath.ToSlash(root)+"\n")...)
		for source, overlay := range dependency.files {
			replacement[filepath.Join(root, filepath.FromSlash(source))] = filepath.Join(dir, "guest", "overlay", overlay)
		}
	}
	overlay, _ := json.Marshal(struct{ Replace map[string]string }{replacement})
	for name, data := range map[string][]byte{"guest.mod": module, "guest.sum": sums, "overlay.json": overlay} {
		if err := os.WriteFile(filepath.Join(temporary, name), data, 0o600); err != nil {
			return err
		}
	}
	wasm := filepath.Join(temporary, "assistant-bash.wasm")
	cmd := exec.Command("go", "build", "-modfile", filepath.Join(temporary, "guest.mod"), "-mod=mod", "-overlay", filepath.Join(temporary, "overlay.json"), "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", wasm, "./guest/cmd")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("构建 assistant-bash: %w", err)
	}
	code, err := os.ReadFile(wasm)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	archive, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := archive.Write(code); err != nil {
		return errors.Join(err, archive.Close())
	}
	if err := archive.Close(); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"assistant-bash.wasm": code, "assistant-bash.wasm.gz": buffer.Bytes()} {
		if err := atomicWrite(filepath.Join(outputDir, name), data); err != nil {
			return err
		}
	}
	return nil
}

func dependencySource(dir, name string) (string, error) {
	download := exec.Command("go", "mod", "download", name)
	download.Dir = dir
	if output, err := download.CombinedOutput(); err != nil {
		return "", fmt.Errorf("下载依赖 %s 失败（离线或网络受限时请预置 GOMODCACHE）: %w: %s", name, err, bytes.TrimSpace(output))
	}
	locate := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", name)
	locate.Dir = dir
	output, err := locate.Output()
	if err != nil {
		return "", fmt.Errorf("定位依赖 %s: %w", name, err)
	}
	source := string(bytes.TrimSpace(output))
	if source == "" {
		return "", fmt.Errorf("定位依赖 %s: go list -m 未给出源码目录（模块未解包）", name)
	}
	return source, nil
}

func atomicWrite(path string, data []byte) (resultErr error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".wasm-*")
	if err != nil {
		return err
	}
	closed := false
	committed := false
	defer func() {
		var closeErr error
		if !closed {
			closeErr = temp.Close()
		}
		var removeErr error
		if !committed {
			removeErr = os.Remove(temp.Name())
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
		}
		resultErr = errors.Join(resultErr, closeErr, removeErr)
	}()
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Chmod(temp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	committed = true
	return nil
}
