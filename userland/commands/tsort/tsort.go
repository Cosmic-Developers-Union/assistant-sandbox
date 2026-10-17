package tsortcmd

import (
	"bufio"
	"container/heap"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

const maxCycleExit = 124

var cmd = &toolcompat.Tool{
	Name:     "tsort",
	Synopsis: "Write totally ordered list consistent with the partial ordering in FILE.",
	Usage:    "tsort [OPTION] [FILE]\n\nWith no FILE, or when FILE is -, read standard input.",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	return runWithOpen(rc, args, func(path string) (io.ReadCloser, error) {
		return vfs.Open(path)
	})
}

type openInputFunc func(path string) (io.ReadCloser, error)

func runWithOpen(rc *toolcompat.RunContext, args []string, openInput openInputFunc) int {
	args = toolcompat.AliasHelpVersion(args)
	fs := toolcompat.NewFlags(cmd.Name)
	warn := fs.BoolP("warnings-are-errors", "w", false, "exit with the number of cycles found")
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	if len(operands) > 1 {
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[1])
	}
	name := "-"
	if len(operands) == 1 {
		name = operands[0]
	}

	var r = rc.In
	var input io.Closer
	if name != "-" {
		f, err := openInput(rc.Path(name))
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "tsort: %s: %v\n", name, pathErr(err))
			return 1
		}
		input = f
		r = f
	}
	closeSource := func() error {
		if input == nil {
			return nil
		}
		err := input.Close()
		input = nil
		return err
	}

	g := newGraph()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	sc.Split(splitToken)
	for sc.Scan() {
		a := sc.Text()
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "tsort: %s: %v\n", name, err)
				_ = closeSource()
				return 1
			}
			g.addNode(a)
			_, _ = fmt.Fprintf(rc.Err, "tsort: %s: input contains an odd number of tokens\n", name)
			if err := closeSource(); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "tsort: %s: %v\n", name, err)
			}
			return 1
		}
		b := sc.Text()
		if a == b {
			g.addNode(a)
		} else {
			g.addEdge(a, b)
		}
	}
	if err := sc.Err(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "tsort: %s: %v\n", name, err)
		_ = closeSource()
		return 1
	}
	if err := closeSource(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "tsort: %s: %v\n", name, err)
		return 1
	}

	bw := bufio.NewWriter(rc.Out)
	cycles := g.topoSort(bw, rc.Err, name)
	if err := bw.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "tsort: write failed: %v\n", err)
		return 1
	}
	if cycles == 0 {
		return 0
	}
	if *warn {
		if cycles > maxCycleExit {
			return maxCycleExit
		}
		return cycles
	}
	return 1
}

func splitToken(data []byte, atEOF bool) (advance int, token []byte, err error) {
	isSeparator := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }
	start := 0
	for start < len(data) && isSeparator(data[start]) {
		start++
	}
	for i := start; i < len(data); i++ {
		if isSeparator(data[i]) {
			return i + 1, data[start:i], nil
		}
	}
	if atEOF {
		if start == len(data) {
			return len(data), nil, nil
		}
		return len(data), data[start:], nil
	}
	return start, nil, nil
}

type graph struct {
	ids   map[string]int
	names []string
	succ  [][]int
	indeg []int
	done  []bool
}

type nodeHeap struct {
	ids   []int
	names []string
}

func (h nodeHeap) Len() int            { return len(h.ids) }
func (h nodeHeap) Less(i, j int) bool  { return h.names[h.ids[i]] < h.names[h.ids[j]] }
func (h nodeHeap) Swap(i, j int)       { h.ids[i], h.ids[j] = h.ids[j], h.ids[i] }
func (h *nodeHeap) Push(x interface{}) { h.ids = append(h.ids, x.(int)) }
func (h *nodeHeap) Pop() interface{} {
	old := h.ids
	n := len(old)
	x := old[n-1]
	h.ids = old[:n-1]
	return x
}

func newGraph() *graph { return &graph{ids: map[string]int{}} }

func (g *graph) addNode(name string) int {
	if id, ok := g.ids[name]; ok {
		return id
	}
	id := len(g.names)
	g.ids[name] = id
	g.names = append(g.names, name)
	g.succ = append(g.succ, nil)
	g.indeg = append(g.indeg, 0)
	g.done = append(g.done, false)
	return id
}

func (g *graph) addEdge(a, b string) {
	u, v := g.addNode(a), g.addNode(b)
	g.succ[u] = append(g.succ[u], v)
	g.indeg[v]++
}

func (g *graph) removeEdge(u, v int) {
	for i, s := range g.succ[u] {
		if s == v {
			g.succ[u] = append(g.succ[u][:i], g.succ[u][i+1:]...)
			g.indeg[v]--
			return
		}
	}
}

func (g *graph) topoSort(out *bufio.Writer, errw io.Writer, name string) int {
	cycles := 0
	h := &nodeHeap{names: g.names}
	for id := range g.names {
		if g.indeg[id] == 0 {
			h.ids = append(h.ids, id)
		}
	}
	heap.Init(h)
	remaining := len(g.names)
	for remaining > 0 {
		if h.Len() == 0 {
			cycle := g.findCycle()
			if len(cycle) == 0 {

				for id := range g.names {
					if !g.done[id] {
						_, _ = out.WriteString(g.names[id])
						_ = out.WriteByte('\n')
						g.done[id] = true
						remaining--
					}
				}
				return 1
			}
			cycles++
			_, _ = fmt.Fprintf(errw, "tsort: %s: input contains a loop:\n", name)
			for _, id := range cycle {
				_, _ = fmt.Fprintf(errw, "tsort: %s\n", g.names[id])
			}
			u, v := cycle[len(cycle)-1], cycle[0]
			g.removeEdge(u, v)
			if g.indeg[v] == 0 {
				heap.Push(h, v)
			}
			continue
		}
		id := heap.Pop(h).(int)
		_, _ = out.WriteString(g.names[id])
		_ = out.WriteByte('\n')
		g.done[id] = true
		remaining--
		for _, s := range g.succ[id] {
			if g.done[s] {
				continue
			}
			g.indeg[s]--
			if g.indeg[s] == 0 {
				heap.Push(h, s)
			}
		}
	}
	return cycles
}

func (g *graph) findCycle() []int {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	state := make([]int8, len(g.names))
	type frame struct {
		id  int
		idx int
	}
	for start := range g.names {
		if g.done[start] || state[start] != white {
			continue
		}
		stack := []frame{{start, 0}}
		state[start] = grey
		for len(stack) > 0 {
			fr := &stack[len(stack)-1]
			advanced := false
			for fr.idx < len(g.succ[fr.id]) {
				next := g.succ[fr.id][fr.idx]
				fr.idx++
				if g.done[next] || state[next] == black {
					continue
				}
				if state[next] == grey {

					var cycle []int
					for i := range stack {
						if stack[i].id == next {
							for _, f := range stack[i:] {
								cycle = append(cycle, f.id)
							}
							break
						}
					}
					return cycle
				}
				state[next] = grey
				stack = append(stack, frame{next, 0})
				advanced = true
				break
			}
			if !advanced {
				state[fr.id] = black
				stack = stack[:len(stack)-1]
			}
		}
	}
	return nil
}

func pathErr(err error) error {
	return toolcompat.SysErr(err)
}
