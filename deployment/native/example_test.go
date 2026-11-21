package native_test

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/deployment/native"
)

// Example 演示只使用公开 API 完成创建 VM、文件访问、连接、执行与
// Shutdown(R01)。
func Example() {
	ctx := context.Background()
	machine, err := native.NewModelVM(ctx, native.Options{Seed: 1})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := machine.Shutdown(ctx); err != nil {
			panic(err)
		}
	}()
	if err := machine.Filesystem().WriteFile(ctx, "note.txt", []byte("hello-file")); err != nil {
		panic(err)
	}
	data, err := machine.Filesystem().ReadFile(ctx, "note.txt")
	if err != nil {
		panic(err)
	}
	terminal, err := machine.Connect(ctx)
	if err != nil {
		panic(err)
	}
	result, err := terminal.Bash().Run(ctx, "printf hello-shell")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(data))
	fmt.Println(result.Output)
	// Output:
	// hello-file
	// hello-shell
}
