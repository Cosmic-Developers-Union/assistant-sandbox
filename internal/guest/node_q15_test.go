package guest_test

import (
	"os"
	"strings"
	"testing"
)

// TestNode22RuntimeManifest 验收 Q15 的固定运行时产物清单:版本、目标
// ABI、V8 模式、ICU 数据路径与归档摘要必须和打包脚本/实际归档一致。
func TestNode22RuntimeManifest(t *testing.T) {
	raw, err := os.ReadFile("testdata/nodejs/node22-runtime.manifest")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(raw)
	for _, want := range []string{
		"node_package=nodejs-22.23.5-r0",
		"target_abi=riscv64gc-lp64d-linux-musl",
		"v8_config=--v8-jitless",
		"icu_data=usr/share/icu/76.1/icudt76l.dat",
		"runtime_archive_sha256=49c1cca5b16b0ada706083d8e50b003a3e79723d9d2e760cf3400e24442210df",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest 缺少 %q", want)
		}
	}
	python312Asset(t, "testdata/nodejs/node22-runtime.tar.gz", node22RuntimeDigest)
	if _, err := os.Stat("testdata/nodejs/build.sh"); err != nil {
		t.Fatal(err)
	}
}
