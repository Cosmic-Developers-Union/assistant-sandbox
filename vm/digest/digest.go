package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func Content(data []byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:", len(data))
	_, _ = h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
