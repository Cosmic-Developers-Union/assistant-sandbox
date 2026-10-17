package base64

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "base64")
	decode := flags.BoolP("decode", "d", false, "解码")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	h := commandio.Context(ctx)
	return commandio.Inputs(ctx, flags.Args(), func(_ string, input string) error {
		var output string
		if *decode {
			raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(input, "\n", ""), "\r", ""))
			if err != nil {
				return err
			}
			output = string(raw)
		} else {
			output = base64.StdEncoding.EncodeToString([]byte(input)) + "\n"
		}
		_, err := fmt.Fprint(h.Stdout, output)
		return err
	})
}
