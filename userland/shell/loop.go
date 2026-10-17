package shell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/protocol"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/messages"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type capture struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int64
	overflow  bool
	cancel    context.CancelFunc
}

func (c *capture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(len(data)) > c.remaining {
		c.overflow = true
		c.cancel()
		return 0, fmt.Errorf("命令输出超限")
	}
	c.remaining -= int64(len(data))
	return c.buffer.Write(data)
}
func (c *capture) reset(limit int64, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buffer.Reset()
	c.remaining = limit
	c.overflow = false
	c.cancel = cancel
}

func Loop(ctx context.Context, kernel *vm.Kernel, parent proc.Proc, input io.Reader, output io.Writer, dir string, environment []string) error {
	var stdout, stderr capture
	session, err := newSessionWithKernel(kernel, parent, dir, environment, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		return err
	}
	defer session.close()
	runner := session.runner
	ctx = proc.WithContext(ctx, session.parent)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	for scanner.Scan() {
		var req protocol.Request
		if err := json.Unmarshal(scanner.Bytes(), &req, json.RejectUnknownMembers(true)); err != nil {
			return fmt.Errorf("解析 Bash 请求: %w", err)
		}
		if req.ID == 0 || len(req.Command) > 64<<10 || req.OutputLimit < 1 || req.OutputLimit > 1<<20 {
			return fmt.Errorf("Bash 请求预算无效")
		}
		commandCtx, cancel := context.WithCancel(messages.WithSnapshot(ctx, req.Messages))
		stdout.reset(req.OutputLimit, cancel)
		stderr.reset(req.OutputLimit, cancel)
		tree, err := parse(req.Command)
		response := protocol.Response{ID: req.ID}
		if err == nil {

			assignment := &syntax.File{Stmts: []*syntax.Stmt{{Cmd: &syntax.DeclClause{Variant: &syntax.Lit{Value: "export"}, Args: []*syntax.Assign{{Name: &syntax.Lit{Value: "MESSAGE"}, Value: &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: req.Message}}}}}}}}}
			err = runner.Run(commandCtx, assignment)
			if err == nil {
				err = runner.Run(commandCtx, tree)
			} else {
				response.Closed = true
			}

			response.Closed = response.Closed || runner.Exited()
		}
		if err != nil {
			if status, ok := errors.AsType[interp.ExitStatus](err); ok {
				response.Code = int(status)
			} else {
				response.Code = 2
				_, _ = fmt.Fprintln(&stderr, err)
			}
		}
		cancel()
		response.Stdout = stdout.buffer.String()
		response.Stderr = stderr.buffer.String()
		response.Overflow = stdout.overflow || stderr.overflow
		if err := json.MarshalWrite(output, response); err != nil {
			return fmt.Errorf("写入 Bash 结果: %w", err)
		}
		if _, err := io.WriteString(output, "\n"); err != nil {
			return err
		}
		if response.Closed || response.Overflow {
			return nil
		}
	}
	return scanner.Err()
}
