package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
)

func main() {
	kernel, parent, err := guest.New("/work", os.Environ(), nil, nil, nil)
	if err == nil {
		err = shell.Loop(context.Background(), kernel, parent, os.Stdin, os.Stdout, "/work", os.Environ())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
