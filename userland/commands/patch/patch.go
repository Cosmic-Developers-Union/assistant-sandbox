package patchcmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/patch"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var cmd = &toolcompat.Tool{
	Name:     "patch",
	Synopsis: "Apply a diff file to one or more original files.",
	Usage: "patch [OPTION]... [ORIGFILE [PATCHFILE]]\n" +
		"Apply unified, context, normal, or ed-format changes from PATCHFILE\n" +
		"(default: standard input) to ORIGFILE (default: the name(s)\n" +
		"recorded in the patch headers).",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

const autoStripSentinel = -1

var cIdentifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var readPromptLine = func() (string, error) {
	return "", errors.New("客体没有 /dev/tty：patch 不能交互提问")
}

func askPrompt(rc *toolcompat.RunContext, prompt string) (string, error) {

	if _, err := io.WriteString(rc.Err, prompt); err != nil {
		return "", err
	}
	return readPromptLine()
}

func run(rc *toolcompat.RunContext, args []string) int {
	out := &stickyWriter{writer: rc.Out}
	errOut := &stickyWriter{writer: rc.Err}
	child := *rc
	child.Out, child.Err = out, errOut
	code := runCore(&child, args)
	if out.err != nil || errOut.err != nil {
		return 2
	}
	return code
}

type stickyWriter struct {
	writer io.Writer
	err    error
}

func (w *stickyWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func writeBytes(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func runCore(rc *toolcompat.RunContext, args []string) int {
	flags := toolcompat.NewFlags(cmd.Name)
	strip := flags.IntP("strip", "p", autoStripSentinel, "strip NUM leading path components from file names (default: use the basename)")
	reverse := flags.BoolP("reverse", "R", false, "assume this patch was created with the old and new files swapped")
	force := flags.BoolP("force", "f", false, "assume the patch is not reversed or already applied, and apply it regardless")
	batch := flags.BoolP("batch", "t", false, "like --force: never treat a hunk as already applied")
	forward := flags.BoolP("forward", "N", false, "silently skip patches that appear reversed or already applied")
	fuzz := flags.IntP("fuzz", "F", 2, "set the fuzz factor (max context lines that may be dropped from a hunk's edges) to NUM")
	ignoreWS := flags.BoolP("ignore-whitespace", "l", false, "compare context/old lines ignoring whitespace differences")
	removeEmpty := flags.BoolP("remove-empty-files", "E", false, "remove output files that become empty after patching")
	quiet := flags.BoolP("quiet", "s", false, "work silently unless an error occurs")
	silent := flags.Bool("silent", false, "same as --quiet")
	backup := flags.BoolP("backup", "b", false, "back up the original contents of each file with a .orig suffix before patching")
	output := flags.StringP("output", "o", "", "output the patched file to FILE instead of patching in place")
	input := flags.StringP("input", "i", "", "read the patch from FILE instead of the PATCHFILE operand or standard input")
	rejectFile := flags.StringP("reject-file", "r", "", "name the reject file NAME instead of <file>.rej (only with a single-file patch)")
	directory := flags.StringP("directory", "d", "", "resolve file names relative to DIR instead of the current directory")
	unified := flags.BoolP("unified", "u", false, "interpret the patch as unified format (auto-detected by default)")
	context := flags.BoolP("context", "c", false, "interpret the patch as context format (auto-detected by default)")
	normal := flags.BoolP("normal", "n", false, "interpret the patch as normal format (auto-detected by default)")
	dryRun := flags.Bool("dry-run", false, "report what would happen without changing any file")
	edFlag := flags.BoolP("ed", "e", false, "interpret the patch as an ed script")
	ifdef := flags.StringP("ifdef", "D", "", "merge using #ifdef NAME instead of patching in place")
	get := flags.IntP("get", "g", autoStripSentinel, "get missing files from RCS/SCCS/... as needed")

	operands, code := toolcompat.Parse(rc, cmd, flags, args)
	if code >= 0 {
		return code
	}
	if flags.Lookup("strip").Changed && *strip < 0 {
		return toolcompat.UsageError(rc, cmd, "-p/--strip requires a non-negative component count")
	}

	if *get != autoStripSentinel {
		return toolcompat.NotSupported(rc, cmd, "-g/--get (RCS/SCCS retrieval)")
	}

	formatHints := 0
	for _, b := range []bool{*unified, *context, *normal, *edFlag} {
		if b {
			formatHints++
		}
	}
	if formatHints > 1 {
		return toolcompat.UsageError(rc, cmd, "only one of -u, -c, -n, -e may be given")
	}

	if len(operands) > 2 {
		return toolcompat.UsageError(rc, cmd, "extra operand %q", operands[2])
	}
	var origFile, patchFileOperand string
	if len(operands) >= 1 {
		origFile = operands[0]
	}
	if len(operands) >= 2 {
		patchFileOperand = operands[1]
	}

	patchPath := *input
	if patchPath == "" {
		patchPath = patchFileOperand
	}
	var data []byte
	var err error
	if patchPath != "" {
		data, err = vfs.ReadFile(resolveOperandPath(rc, *directory, patchPath))
	} else {
		data, err = io.ReadAll(rc.In)
	}
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
		return 2
	}
	if *ifdef != "" && !cIdentifierRE.MatchString(*ifdef) {
		return toolcompat.UsageError(rc, cmd, "invalid -D define %q", *ifdef)
	}
	edScript, edIndex, looksEd := patch.ExtractEdScript(data)
	if *edFlag {
		if *reverse {
			return toolcompat.UsageError(rc, cmd, "-R cannot be used with -e")
		}
		if looksEd {
			data = edScript
		}
		return runEdScript(rc, data, origFile, edIndex, *strip, *directory, *output, *rejectFile, *backup, *dryRun, *ifdef)
	}
	if formatHints == 0 && looksEd {
		return runEdScript(rc, edScript, origFile, edIndex, *strip, *directory, *output, *rejectFile, *backup, *dryRun, *ifdef)
	}

	parsed, err := patch.Parse(data)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "%s: %v\n", cmd.Name, err)
		return 2
	}
	wantFormat := patch.Format(-1)
	switch {
	case *unified:
		wantFormat = patch.FormatUnified
	case *context:
		wantFormat = patch.FormatContext
	case *normal:
		wantFormat = patch.FormatNormal
	}
	if wantFormat >= 0 {
		for _, fp := range parsed.Files {
			if fp.Format != wantFormat {
				_, _ = fmt.Fprintf(rc.Err, "%s: input is %s format, not requested %s format\n", cmd.Name, fp.Format, wantFormat)
				return 2
			}
		}
	}

	if *rejectFile != "" && len(parsed.Files) > 1 {
		return toolcompat.UsageError(rc, cmd, "-r/--reject-file requires a patch that touches exactly one file")
	}
	if origFile != "" && len(parsed.Files) > 1 {
		return toolcompat.UsageError(rc, cmd, "a FILE operand cannot be combined with a multi-file patch")
	}

	ro := runOptions{
		strip:         *strip,
		reverse:       *reverse,
		directory:     *directory,
		origFile:      origFile,
		output:        *output,
		rejectFile:    *rejectFile,
		quiet:         *quiet || *silent,
		backup:        *backup,
		removeEmpty:   *removeEmpty,
		dryRun:        *dryRun,
		force:         *force,
		batch:         *batch,
		forward:       *forward,
		define:        *ifdef,
		backedUp:      make(map[string]bool),
		intermediate:  make(map[string][]byte),
		rejectStarted: make(map[string]bool),
		reverseState:  new(bool),
		reverseProbe:  func() *bool { v := true; return &v }(),
		hardError:     new(bool),
		applyOpts: patch.ApplyOptions{
			Reverse:          *reverse,
			Fuzz:             *fuzz,
			IgnoreWhitespace: *ignoreWS,

			Force: *force || *batch || !*forward,
		},
	}
	if *output != "" {
		ro.aggregate = &bytes.Buffer{}
	}

	trouble := false
	for _, fp := range parsed.Files {
		if fp.Unsupported != "" {
			_, _ = fmt.Fprintf(rc.Err, "%s: %s: %s\n", cmd.Name, filePatchLabel(fp), fp.Unsupported)
			*ro.hardError = true
			trouble = true
			continue
		}
		if !applyOneFile(rc, ro, fp) {
			trouble = true
		}
	}
	if ro.aggregate != nil && !ro.dryRun {
		if !writeAggregateOutput(rc, ro, ro.aggregate.Bytes()) {
			trouble = true
		}
	}
	if *ro.hardError {
		return 2
	}
	if trouble {
		return 1
	}
	return 0
}

type runOptions struct {
	strip         int
	reverse       bool
	directory     string
	origFile      string
	output        string
	rejectFile    string
	quiet         bool
	backup        bool
	removeEmpty   bool
	dryRun        bool
	force         bool
	batch         bool
	forward       bool
	define        string
	backedUp      map[string]bool
	intermediate  map[string][]byte
	rejectStarted map[string]bool
	reverseState  *bool
	reverseProbe  *bool
	hardError     *bool
	aggregate     *bytes.Buffer
	applyOpts     patch.ApplyOptions
}

func filePatchLabel(fp patch.FilePatch) string {
	switch {
	case fp.NewName != "" && fp.NewName != patch.DevNull:
		return fp.NewName
	case fp.OldName != "" && fp.OldName != patch.DevNull:
		return fp.OldName
	case fp.RenameTo != "":
		return fp.RenameTo
	case fp.RenameFrom != "":
		return fp.RenameFrom
	default:
		return "<unknown>"
	}
}

func describeErr(err error) string {
	pe := &fs.PathError{}
	if errors.As(err, &pe) {
		return fmt.Sprintf("%s: %v", pe.Path, pe.Err)
	}
	return err.Error()
}

func runEdScript(rc *toolcompat.RunContext, script []byte, origFile, indexName string, strip int, directory, output, rejectFile string, backup, dryRun bool, define string) int {
	if origFile == "" && indexName != "" {
		candidate := targetName(indexName, strip)
		if candidate != "" {
			path := resolveOperandPath(rc, directory, candidate)
			if fi, err := vfs.Stat(path); err == nil && !fi.IsDir() {
				origFile = candidate
			}
		}
	}
	if origFile == "" {
		var err error
		origFile, err = askPrompt(rc, "File to patch: ")
		if err != nil || origFile == "" {
			_, _ = fmt.Fprintf(rc.Err, "%s: cannot determine file to patch: %v\n", cmd.Name, err)
			return 2
		}
	}
	target := resolveOperandPath(rc, directory, origFile)
	content, err := vfs.ReadFile(target)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
		return 2
	}
	var result []byte
	if define != "" {
		result, err = patch.ApplyEdIfdef(content, script, define)
	} else {
		result, err = patch.ApplyEd(content, script)
	}
	var rejected *patch.EdRejectError
	if err != nil && !errors.As(err, &rejected) {
		_, _ = fmt.Fprintf(rc.Err, "%s: %v\n", cmd.Name, err)
		return 2
	}
	if dryRun {
		if rejected != nil {
			return 1
		}
		return 0
	}
	outPath := target
	if output != "" {
		outPath = resolveOperandPath(rc, directory, output)
	}
	fi, statErr := vfs.Stat(outPath)
	existed := statErr == nil
	mode := fs.FileMode(0o644)
	if existed {
		mode = fi.Mode().Perm()
	}
	writeOutput := output != "" || rejected == nil || rejected.Applied
	if backup && existed && writeOutput {
		if err := backupFile(outPath); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: backup failed: %s\n", cmd.Name, describeErr(err))
			return 2
		}
	}
	if writeOutput {
		if err := writeFileContent(rc, outPath, result, mode, existed); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
			return 2
		}
	}
	if rejected != nil {
		rejPath := outPath + ".rej"
		if rejectFile != "" {
			rejPath = resolveOperandPath(rc, directory, rejectFile)
		}
		rejData := patch.WriteRejectFormat("", "", patch.FormatNormal, rejected.Hunks)
		rej, writeErr := vfs.OpenFile(rejPath, int(vfs.O_WRONLY|vfs.O_CREATE|vfs.O_TRUNC), 0o644)
		if writeErr == nil {
			writeErr = writeBytes(rej, rejData)
			if closeErr := rej.Close(); writeErr == nil {
				writeErr = closeErr
			}
		}
		if writeErr != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: could not write reject file: %s\n", cmd.Name, describeErr(writeErr))
			return 2
		}
		_, _ = fmt.Fprintf(rc.Err, "%d ed-style differences failed -- saving rejects to file %s\n", len(rejected.Hunks), rejPath)
		return 1
	}
	return 0
}

func writeAggregateOutput(rc *toolcompat.RunContext, ro runOptions, content []byte) bool {
	target := resolveOperandPath(rc, ro.directory, ro.output)
	fi, err := vfs.Stat(target)
	existed := err == nil
	if err != nil && !vfs.NotExist(err) {
		_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
		return hardFailure(ro)
	}
	mode := fs.FileMode(0o644)
	if existed {
		mode = fi.Mode().Perm()
	}
	if ro.backup && existed {
		if err := backupOnce(target, ro.backedUp); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: backup failed: %s\n", cmd.Name, describeErr(err))
			return hardFailure(ro)
		}
	}
	if err := writeFileContent(rc, target, content, mode, existed); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
		return hardFailure(ro)
	}
	return true
}

func hardFailure(ro runOptions) bool {
	if ro.hardError != nil {
		*ro.hardError = true
	}
	return false
}

func applyOneFile(rc *toolcompat.RunContext, ro runOptions, fp patch.FilePatch) bool {
	if ro.reverseState != nil && *ro.reverseState {
		ro.reverse = true
		ro.applyOpts.Reverse = true
	}
	oldName, newName := fp.OldName, fp.NewName
	if ro.reverse {
		oldName, newName = newName, oldName
	}
	createMode := oldName == patch.DevNull && newName != patch.DevNull
	deleteMode := newName == patch.DevNull && oldName != patch.DevNull

	displayName := oldName
	if createMode {
		displayName = newName
	}
	if displayName == "" || displayName == patch.DevNull {
		if oldName != "" && oldName != patch.DevNull {
			displayName = oldName
		}
	}

	var targetPath string
	if ro.origFile != "" {
		targetPath = resolveOperandPath(rc, ro.directory, ro.origFile)
	} else {
		var found bool
		targetPath, displayName, found = resolveHeaderTarget(rc, ro.directory, oldName, newName, fp.IndexName, ro.strip, createMode, ro.intermediate)
		if !found && !createMode {
			if ro.force || ro.batch {
				_, _ = fmt.Fprintf(rc.Err, "%s: can't find file to patch\n", cmd.Name)
				return hardFailure(ro)
			}
			answer, err := askPrompt(rc, "File to patch: ")
			if err != nil || answer == "" {
				_, _ = fmt.Fprintf(rc.Err, "%s: cannot determine file to patch: %v\n", cmd.Name, err)
				return hardFailure(ro)
			}
			targetPath = resolveOperandPath(rc, ro.directory, answer)
			displayName = answer
		}
	}

	var oldLines []string
	var oldNoEOL bool
	existed := false
	var origMode fs.FileMode = 0o644

	if !createMode {
		content, cached := ro.intermediate[targetPath]
		if !cached {
			fi, statErr := vfs.Stat(targetPath)
			if statErr != nil {
				_, _ = fmt.Fprintf(rc.Err, "%s: can't find file to patch: %s\n", cmd.Name, describeErr(statErr))
				return hardFailure(ro)
			}
			origMode = fi.Mode().Perm()
			var err error
			content, err = vfs.ReadFile(targetPath)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
				return hardFailure(ro)
			}
		}
		existed = true
		oldLines, oldNoEOL = splitFileLines(content)
	} else if content, cached := ro.intermediate[targetPath]; cached {
		existed = true
		oldLines, oldNoEOL = splitFileLines(content)
	} else if fi, statErr := vfs.Stat(targetPath); statErr == nil {
		if fi.IsDir() {
			_, _ = fmt.Fprintf(rc.Err, "%s: can't patch directory %s\n", cmd.Name, displayName)
			return hardFailure(ro)
		}
		origMode = fi.Mode().Perm()
		content, err := vfs.ReadFile(targetPath)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
			return hardFailure(ro)
		}
		existed = true
		oldLines, oldNoEOL = splitFileLines(content)
	} else if !vfs.NotExist(statErr) {
		_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(statErr))
		return hardFailure(ro)
	}

	creationConflict := createMode && (len(oldLines) != 0 || oldNoEOL)
	res, autoReversed := applyWithPolicy(rc, oldLines, oldNoEOL, fp, ro, creationConflict)
	if autoReversed {
		oldName, newName = newName, oldName
		createMode = oldName == patch.DevNull && newName != patch.DevNull
		deleteMode = newName == patch.DevNull && oldName != patch.DevNull
		if ro.reverseState != nil {
			*ro.reverseState = true
		}
	}

	if !ro.quiet {
		verb := "patching file"
		if createMode {
			verb = "creating file"
		} else if deleteMode {
			verb = "removing file"
		}
		_, _ = fmt.Fprintf(rc.Err, "%s %s%s\n", verb, displayName, dryRunSuffix(ro.dryRun))
	}
	failed := 0
	changed := false
	ignoredAlready := false
	for _, r := range res.Reports {
		switch r.Outcome {
		case patch.HunkApplied:
			changed = true
		case patch.HunkAppliedFuzzy:
			changed = true
			if !ro.quiet {
				_, _ = fmt.Fprintf(rc.Err, "Hunk #%d succeeded at %d with fuzz %d.\n", r.Index, r.At+1, r.FuzzUsed)
			}
		case patch.HunkAlreadyApplied:
			ignoredAlready = true
			if !ro.quiet {
				_, _ = fmt.Fprintf(rc.Err, "Hunk #%d ignored -- already applied.\n", r.Index)
			}
		case patch.HunkFailed:
			failed++
			_, _ = fmt.Fprintf(rc.Err, "Hunk #%d FAILED.\n", r.Index)
		}
	}
	allApplied := res.AllApplied()

	success := allApplied && !ignoredAlready
	if allApplied && ro.reverseProbe != nil {
		*ro.reverseProbe = false
	}

	if ro.dryRun {
		return success
	}

	finalEmpty := len(res.Lines) == 0
	removeFile := allApplied && ro.output == "" && ro.define == "" && (deleteMode || (ro.removeEmpty && existed && finalEmpty && !deleteMode))

	outPath := targetPath
	if ro.aggregate != nil {
		content := joinFileLines(res.Lines, res.NoFinalNewline)
		ro.intermediate[targetPath] = append([]byte(nil), content...)
		_, _ = ro.aggregate.Write(content)
	}
	outInfo, outStatErr := vfs.Stat(outPath)
	outExisted := outStatErr == nil
	if outStatErr != nil && !vfs.NotExist(outStatErr) {
		_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(outStatErr))
		return hardFailure(ro)
	}

	switch {
	case ro.aggregate != nil:

	case !changed:

	case removeFile:
		if ro.backup && existed {
			if err := backupOnce(targetPath, ro.backedUp); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "%s: backup failed: %s\n", cmd.Name, describeErr(err))
				return hardFailure(ro)
			}
		}
		if err := vfs.Remove(targetPath); err != nil && !vfs.NotExist(err) {
			_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
			return hardFailure(ro)
		}
	default:
		if ro.backup && outExisted {
			if err := backupOnce(outPath, ro.backedUp); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "%s: backup failed: %s\n", cmd.Name, describeErr(err))
				return hardFailure(ro)
			}
		}
		content := joinFileLines(res.Lines, res.NoFinalNewline)
		outMode := origMode
		if outExisted {
			outMode = outInfo.Mode().Perm()
		}
		if err := writeFileContent(rc, outPath, content, outMode, outExisted); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", cmd.Name, describeErr(err))
			return hardFailure(ro)
		}
	}

	if !allApplied {
		rejPath := outPath + ".rej"
		if ro.output != "" {
			rejPath = resolveOperandPath(rc, ro.directory, ro.output) + ".rej"
		}
		if ro.rejectFile != "" {
			rejPath = resolveOperandPath(rc, ro.directory, ro.rejectFile)
		}

		rejData := patch.WriteRejectFormat("", "", fp.Format, res.Rejects)
		flags := int(vfs.O_WRONLY | vfs.O_CREATE | vfs.O_TRUNC)
		if ro.rejectStarted[rejPath] {
			flags = int(vfs.O_WRONLY | vfs.O_CREATE | vfs.O_APPEND)
		}
		rej, err := vfs.OpenFile(rejPath, flags, 0o644)
		if err == nil {
			err = writeBytes(rej, rejData)
			if closeErr := rej.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "%s: could not write reject file: %s\n", cmd.Name, describeErr(err))
			return hardFailure(ro)
		}
		ro.rejectStarted[rejPath] = true
		_, _ = fmt.Fprintf(rc.Err, "%d out of %d hunks failed -- saving rejects to file %s\n", failed, len(fp.Hunks), rejPath)
	}
	return success
}

func applyWithPolicy(rc *toolcompat.RunContext, oldLines []string, oldNoEOL bool, fp patch.FilePatch, ro runOptions, creationConflict bool) (patch.Result, bool) {
	apply := func(opts patch.ApplyOptions) patch.Result {
		if ro.define != "" {
			return patch.ApplyIfdef(oldLines, oldNoEOL, fp.Hunks, opts, ro.define)
		}
		return patch.Apply(oldLines, oldNoEOL, fp.Hunks, opts)
	}
	if ro.reverse || ro.force || ro.batch {
		return apply(ro.applyOpts), false
	}
	if ro.forward {
		if !creationConflict {
			return apply(ro.applyOpts), false
		}
		reverseOpts := ro.applyOpts
		reverseOpts.Reverse = true
		if reversed := apply(reverseOpts); reversed.AllApplied() {
			reports := make([]patch.HunkReport, len(fp.Hunks))
			for i := range reports {
				reports[i] = patch.HunkReport{Index: i + 1, Outcome: patch.HunkAlreadyApplied}
			}
			return patch.Result{Lines: append([]string(nil), oldLines...), NoFinalNewline: oldNoEOL, Reports: reports}, false
		}
		return failedResult(oldLines, oldNoEOL, fp.Hunks), false
	}
	forwardOpts := ro.applyOpts
	forwardOpts.Force = true
	forward := failedResult(oldLines, oldNoEOL, fp.Hunks)
	if !creationConflict {
		forward = apply(forwardOpts)
	}
	if len(forward.Reports) == 0 || forward.Reports[0].Outcome != patch.HunkFailed {
		return forward, false
	}
	if ro.reverseProbe != nil && !*ro.reverseProbe {
		return forward, false
	}
	reverseOpts := forwardOpts
	reverseOpts.Reverse = true
	reversed := apply(reverseOpts)
	if !reversed.AllApplied() {
		return forward, false
	}
	answer, err := askPrompt(rc, "Reversed (or previously applied) patch detected! Assume -R? [n] ")
	yes, matchErr := locale.MatchAffirmative(rc.Env, answer)
	if err == nil && matchErr == nil && yes {
		return reversed, true
	}
	if matchErr != nil {
		_, _ = fmt.Fprintf(rc.Err, "%s: %v\n", cmd.Name, matchErr)
	}
	return forward, false
}

func failedResult(oldLines []string, oldNoEOL bool, hunks []patch.Hunk) patch.Result {
	reports := make([]patch.HunkReport, len(hunks))
	for i := range reports {
		reports[i] = patch.HunkReport{Index: i + 1, Outcome: patch.HunkFailed}
	}
	return patch.Result{
		Lines:          append([]string(nil), oldLines...),
		NoFinalNewline: oldNoEOL,
		Reports:        reports,
		Rejects:        append([]patch.Hunk(nil), hunks...),
	}
}

func dryRunSuffix(dryRun bool) string {
	if dryRun {
		return " (dry run)"
	}
	return ""
}

func splitFileLines(content []byte) (lines []string, noFinalNewline bool) {
	s := string(content)
	if s == "" {
		return nil, false
	}
	trailingNL := strings.HasSuffix(s, "\n")
	parts := strings.Split(s, "\n")
	if trailingNL {
		parts = parts[:len(parts)-1]
	}
	return parts, !trailingNL
}

func joinFileLines(lines []string, noFinalNewline bool) []byte {
	if len(lines) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for i, l := range lines {
		buf.WriteString(l)
		if i < len(lines)-1 || !noFinalNewline {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

func targetName(name string, strip int) string {
	slashed := name
	if strip < 0 {
		return baseComponent(slashed)
	}
	return stripComponents(slashed, strip)
}

func resolveTargetName(rc *toolcompat.RunContext, dir, name string, strip int) string {
	stripped := targetName(name, strip)
	if stripped == "" {
		return ""
	}
	return resolveOperandPath(rc, dir, stripped)
}

func resolveHeaderTarget(rc *toolcompat.RunContext, dir, oldName, newName, indexName string, strip int, create bool, intermediate map[string][]byte) (string, string, bool) {
	for _, name := range []string{oldName, newName, indexName} {
		if name == "" || name == patch.DevNull {
			continue
		}
		path := resolveTargetName(rc, dir, name, strip)
		if path == "" {
			continue
		}
		if _, ok := intermediate[path]; ok {
			return path, targetName(name, strip), true
		}
		if fi, err := vfs.Stat(path); err == nil && !fi.IsDir() {
			return path, targetName(name, strip), true
		}
	}
	if create {
		path := resolveTargetName(rc, dir, newName, strip)
		return path, targetName(newName, strip), path != ""
	}
	name := oldName
	if name == "" || name == patch.DevNull {
		name = newName
	}
	if (name == "" || name == patch.DevNull) && indexName != "" {
		name = indexName
	}
	if name == "" || name == patch.DevNull {
		return "", "", false
	}
	path := resolveTargetName(rc, dir, name, strip)
	return path, name, false
}

func resolveOperandPath(rc *toolcompat.RunContext, dir, name string) string {
	if path.IsAbs(name) {
		return rc.Path(name)
	}
	return rc.Path(path.Join(dir, name))
}

func stripComponents(name string, n int) string {
	if n <= 0 {
		return name
	}
	rest := name
	if strings.HasPrefix(rest, "/") {
		rest = strings.TrimLeft(rest, "/")
		n--
	}
	for n > 0 {
		i := strings.Index(rest, "/")
		if i < 0 {
			return ""
		}
		rest = strings.TrimLeft(rest[i:], "/")
		n--
	}
	return rest
}

func baseComponent(name string) string {
	trimmed := strings.TrimRight(name, "/")
	if trimmed == "" {
		return ""
	}
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

func backupFile(target string) error {
	backup := target + ".orig"
	content, err := vfs.ReadFile(target)
	if err != nil {
		return err
	}
	fi, err := vfs.Stat(target)
	if err != nil {
		return err
	}
	return vfs.WriteFile(backup, content, fi.Mode().Perm())
}

func backupOnce(target string, backedUp map[string]bool) error {
	if backedUp[target] {
		return nil
	}
	if err := backupFile(target); err != nil {
		return err
	}
	backedUp[target] = true
	return nil
}

func writeFileContent(rc *toolcompat.RunContext, target string, content []byte, perm fs.FileMode, existed bool) error {
	if !existed {
		if err := vfs.MkdirAll(path.Dir(target), 0o777); err != nil {
			return err
		}
		f, err := vfs.OpenFile(target, int(vfs.O_WRONLY|vfs.O_CREATE|vfs.O_TRUNC), perm)
		if err != nil {
			return err
		}
		if err := writeBytes(f, content); err != nil {
			return errors.Join(err, f.Close())
		}
		return f.Close()
	}

	tmp, tmpName, err := createTemp(path.Dir(target), ".patch-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = vfs.Remove(tmpName)
		}
	}()
	if err := writeBytes(tmp, content); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := vfs.Rename(tmpName, target); err != nil {
		return err
	}
	keep = true
	return nil
}

func createTemp(dir, prefix string) (vmfs.File, string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		name := path.Join(dir, fmt.Sprintf("%s%d", prefix, attempt))
		file, err := vfs.OpenFile(name, int(vfs.O_WRONLY|vfs.O_CREATE|vfs.O_EXCL), 0o600)
		if err == nil {
			return file, name, nil
		}
		if !vfs.Exist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("无法创建临时文件 %s*", prefix)
}
