package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	programs, err := executable.New(map[string]executable.Program{"probe": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		network, err := p.Resources.Network()
		if err != nil {
			return 0, err
		}
		_, err = network.Dial(ctx, "tcp", "example.invalid:80")
		if !errors.Is(err, errno.NetworkUnreachable) {
			return 0, fmt.Errorf("网络默认行为错误: %v", err)
		}
		communication, err := p.Resources.Communication()
		if err != nil {
			return 0, err
		}
		_, err = communication.Map(ctx, "shared", 4096)
		if !errors.Is(err, errno.NotImplemented) {
			return 0, fmt.Errorf("IPC默认行为错误: %v", err)
		}
		_, err = communication.Open(ctx, ipc.Socket, "socket")
		if !errors.Is(err, errno.NotImplemented) {
			return 0, err
		}
		return 7, nil
	})})
	if err != nil {
		return err
	}
	kernel, err := vm.New(vm.Services{Programs: programs}, 2)
	if err != nil {
		return err
	}
	p, err := kernel.Exec(context.Background(), "probe", nil)
	if err != nil {
		return err
	}
	if _, err := kernel.Reap(p.ID); err != nil {
		return err
	}
	if len(kernel.Processes()) != 1 {
		return fmt.Errorf("进程回收失败")
	}
	fmt.Printf("vm: pid=%d parent=%d exit=%d net=ENETUNREACH ipc=ENOSYS\n", p.ID, p.Parent, p.ExitCode)
	return nil
}
