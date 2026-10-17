//go:build ignore

package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	switch os.Args[1] {
	case "echo":
		dir, _ := os.Getwd()
		fmt.Println(dir, os.Getenv("VALUE"))
		io.Copy(os.Stdout, os.Stdin)
	case "write":
		if err := os.WriteFile("extension.txt", []byte("extension"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "exit":
		fmt.Fprintln(os.Stderr, "expected failure")
		os.Exit(7)
	case "spin":
		for {
		}
	case "large":
		for range 400000 {
			fmt.Print("x")
		}
	}
}
