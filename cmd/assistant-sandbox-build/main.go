package main

import (
	"fmt"
	"os"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/build"
)

func main() {
	if err := build.Generate(".", "."); err != nil {
		fmt.Fprintln(os.Stderr, "生成 WASI 客体:", err)
		os.Exit(1)
	}
}
