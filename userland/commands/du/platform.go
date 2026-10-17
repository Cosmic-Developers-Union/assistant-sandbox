package ducmd

import (
	"io/fs"
)

const haveSysTimes = false

func (d *duRun) usage(fi fs.FileInfo) int64 { return fi.Size() }

func (d *duRun) skipHardlink(fs.FileInfo) bool { return false }

func fileDev(fs.FileInfo) (uint64, bool) { return 0, false }
