package guest_test

import (
	"testing"
	"time"
)

const node22TestsDigest = "60664e78353a70164a2a6279eae4f3b14418d594defba2b54a7edd1f09d998c8"

// TestNode22UpstreamTestSlice 验证 Q27:固定 Node 22.23.5 源码树中的
// common 测试支撑与目标模块测试在客体 Node 内可重复运行。
func TestNode22UpstreamTestSlice(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	extractGuestTar(t, machine, python312Asset(t,
		"testdata/nodejs/node22-tests.tar.gz", node22TestsDigest))
	files := []string{
		"test-assert.js",
		"test-path.js",
		"test-querystring.js",
		"test-util-format.js",
	}
	for _, file := range files {
		output, diagnostic, code := runNodeInDir(t, connection, "/work/node-tests/parallel",
			[]string{"--jitless", file}, 360*time.Second, nil)
		if code != 0 {
			t.Fatalf("Node 上游分片 %s exit=%d stdout=%q stderr=%q", file, code, output, diagnostic)
		}
		t.Logf("Q27-PASS %s", file)
	}
}
