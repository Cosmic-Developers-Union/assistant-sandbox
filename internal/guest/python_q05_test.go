package guest_test

import (
	"testing"
	"time"
)

// TestPython312FileAndCompression 验证 Q05:pathlib、tempfile、
// zipfile 与 gzip 在客体工作区完成读写和清理。
func TestPython312FileAndCompression(t *testing.T) {
	_, connection := python312Guest(t)
	script := `import pathlib, tempfile, zipfile, gzip, shutil
base = pathlib.Path("/work/q05")
if base.exists():
    shutil.rmtree(base)
base.mkdir()
text = base / "hello.txt"
text.write_text("hello\n", encoding="utf-8")
assert text.read_text(encoding="utf-8") == "hello\n"
assert sorted(p.name for p in base.iterdir()) == ["hello.txt"]
with tempfile.TemporaryDirectory(dir="/work") as td:
    tmp = pathlib.Path(td) / "tmp.txt"
    tmp.write_bytes(b"abc")
    assert tmp.read_bytes() == b"abc"
assert not pathlib.Path(td).exists()
archive = base / "a.zip"
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
    z.writestr("dir/x.txt", "zip-data")
with zipfile.ZipFile(archive, "r") as z:
    assert z.namelist() == ["dir/x.txt"]
    assert z.read("dir/x.txt") == b"zip-data"
    assert z.testzip() is None
gz = base / "a.gz"
with gzip.open(gz, "wt", encoding="utf-8") as f:
    f.write("gzip-data")
with gzip.open(gz, "rt", encoding="utf-8") as f:
    assert f.read() == "gzip-data"
for p in (text, archive, gz):
    p.unlink()
base.rmdir()
assert not base.exists()
print("Q05-OK")
`
	output, code := runPython312(t, connection, script, 180*time.Second)
	if code != 0 || output != "Q05-OK\n" {
		t.Fatalf("文件与压缩模块 exit=%d output=%q", code, output)
	}
}
