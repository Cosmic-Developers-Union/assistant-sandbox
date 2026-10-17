package commcmd

import (
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestEnvironmentHelpers(t *testing.T) {
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
