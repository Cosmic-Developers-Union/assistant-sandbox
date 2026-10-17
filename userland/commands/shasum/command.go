package shasum

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "shasum")
	algorithm := flags.IntP("algorithm", "a", 1, "摘要算法")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	var newHash func() hash.Hash
	switch *algorithm {
	case 1:
		newHash = sha1.New
	case 256:
		newHash = sha256.New
	case 512:
		newHash = sha512.New
	default:
		return fmt.Errorf("shasum 未支持算法")
	}
	h := commandio.Context(ctx)
	return commandio.Inputs(ctx, flags.Args(), func(name, input string) error {
		digest := newHash()
		digest.Write([]byte(input))
		_, err := fmt.Fprintf(h.Stdout, "%x  %s\n", digest.Sum(nil), name)
		return err
	})
}
