package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func adaptInterpreter(root string) error {
	edits := map[string]func(string) (string, error){
		"vars.go": func(source string) (string, error) {
			source = strings.Replace(source, "\tcryptorand \"crypto/rand\"\n", "", 1)
			source = strings.Replace(source, "\t\"os\"\n", "\tvmproc \"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc\"\n", 1)
			source = strings.Replace(source, "strconv.Itoa(os.Getpid())", "r.vmPID(false)", 1)
			source = strings.Replace(source, "strconv.Itoa(os.Getppid())", "r.vmPID(true)", 1)
			source = strings.Replace(source, "mathrand.IntN(randomMax)", "int(r.vmRandom() % randomMax)", 1)
			var err error
			source, err = replaceRegion(source, "\tcase \"SRANDOM\":", "\tcase \"DIRSTACK\":", "\tcase \"SRANDOM\":\n\t\tvr.Kind, vr.Str = expand.String, strconv.FormatUint(uint64(r.vmRandom()), 10)\n")
			if err != nil {
				return "", err
			}
			return source + interpreterResources, nil
		},
		"runner.go": func(source string) (string, error) {
			source = strings.Replace(source, "\t\"fmt\"\n", "\t\"fmt\"\n\tvmerrno \"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno\"\n", 1)
			source = strings.Replace(source, "\tmathrand \"math/rand/v2\"\n", "", 1)
			source = strings.Replace(source, "\t\"runtime\"\n", "", 1)
			source = strings.Replace(source, "\t\"path/filepath\"\n", "", 1)
			var err error
			source, err = replaceRegion(source, "\t\tProcSubst: func(ps *syntax.ProcSubst)", "\n\t}\n\tr.updateExpandOpts()", "\t\tProcSubst: func(ps *syntax.ProcSubst) (string,error) { return \"\", vmerrno.NotImplemented },")
			if err != nil {
				return "", err
			}
			markerExpand := "\tcase unsetParam, errMsg == \"invalid indirect expansion\":"
			if strings.Count(source, markerExpand) != 1 {
				return "", fmt.Errorf("展开错误适配锚点不唯一")
			}
			source = strings.Replace(source, markerExpand, "\tcase errors.Is(err, vmerrno.NotImplemented):\n\t\tr.exit.fatal(err)\n"+markerExpand, 1)
			source, err = replaceRegion(source, "\tcase *syntax.TimeClause:", "\tdefault:\n\t\t// Should only happen if we forgot a case above.", "\tcase *syntax.TimeClause:\n\t\tr.exit.fatal(fmt.Errorf(\"VM 尚未提供语言计时接口\"))\n")
			if err != nil {
				return "", err
			}
			source, err = replaceRegion(source, "\tdir, name := filepath.Split(path)", "\tf, err := r.openHandler", "")
			if err != nil {
				return "", err
			}
			marker := "\tif st.Background || st.Disown {"
			if strings.Count(source, marker) != 1 {
				return "", fmt.Errorf("后台任务适配锚点不唯一")
			}
			source = strings.Replace(source, marker, "\tif st.Background || st.Disown || st.Coprocess { r.exit.fatal(fmt.Errorf(\"VM 不支持后台任务\")); return }\n"+marker, 1)
			return source, nil
		},
		"builtin.go": func(source string) (string, error) {
			marker := "pwd, err = filepath.EvalSymlinks(pwd)"
			if strings.Count(source, marker) != 1 {
				return "", fmt.Errorf("物理路径适配锚点不唯一")
			}
			source = strings.Replace(source, marker, "err = fmt.Errorf(\"VM 尚未提供物理路径解析\")", 1)

			source = strings.ReplaceAll(source, "r.stdin.SetReadDeadline(time.Now())", "r.stdin.SetReadDeadline(time.Time{})")
			if !strings.Contains(source, "filepath.") {
				source = strings.Replace(source, "\t\"path/filepath\"\n", "", 1)
			}
			return source, nil
		},
	}
	for name, edit := range edits {
		file := filepath.Join(root, "interp", name)
		original, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		source, err := edit(string(original))
		if err != nil {
			return fmt.Errorf("适配解释器 %s: %w", name, err)
		}
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			return err
		}
	}
	return nil
}

func replaceRegion(source, start, end, replacement string) (string, error) {
	before, rest, ok := strings.Cut(source, start)
	if !ok {
		return "", fmt.Errorf("解释器缺少起始锚点 %q", start)
	}
	_, after, ok := strings.Cut(rest, end)
	if !ok {
		return "", fmt.Errorf("解释器缺少结束锚点 %q", end)
	}
	return before + replacement + end + after, nil
}

const interpreterResources = `
// VM 扩展使用执行上下文中的客体身份和随机源，缺失能力明确终止解释器。
func (r *Runner) vmPID(parent bool) string {
 p,err:=vmproc.FromContext(r.ectx);if err!=nil{r.exit.fatal(err);return "0"}
 if parent{return strconv.FormatUint(uint64(p.Parent),10)}
 return strconv.FormatUint(uint64(p.ID),10)
}
func (r *Runner) vmRandom() uint32 {
 p,err:=vmproc.FromContext(r.ectx);if err!=nil{r.exit.fatal(err);return 0}
 source,err:=p.Resources.Random();if err!=nil{r.exit.fatal(err);return 0}
 var data [4]byte
 n,err:=source.Read(r.ectx,data[:]);if err!=nil{r.exit.fatal(err);return 0}
 if n!=len(data){r.exit.fatal(fmt.Errorf("VM 随机源返回长度错误"));return 0}
 return binary.LittleEndian.Uint32(data[:])
}
`
