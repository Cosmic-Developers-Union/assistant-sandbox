package patch

type Format int

const (
	FormatUnified Format = iota
	FormatContext
	FormatNormal
)

func (f Format) String() string {
	switch f {
	case FormatUnified:
		return "unified"
	case FormatContext:
		return "context"
	case FormatNormal:
		return "normal"
	default:
		return "unknown"
	}
}

type LineKind int

const (
	LineContext LineKind = iota
	LineDelete
	LineAdd
)

type HunkLine struct {
	Kind  LineKind
	Text  string
	NoEOL bool
}

type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Lines              []HunkLine
}

type FilePatch struct {
	OldName string
	NewName string

	IndexName string
	Format    Format
	Hunks     []Hunk

	RenameFrom, RenameTo string

	Unsupported string
}

type Patch struct {
	Files []FilePatch
}

func (fp *FilePatch) IsCreate() bool { return fp.OldName == DevNull && fp.NewName != DevNull }

func (fp *FilePatch) IsDelete() bool { return fp.NewName == DevNull && fp.OldName != DevNull }

const DevNull = "/dev/null"
