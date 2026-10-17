package testcmd

import (
	"errors"
	"io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var errUnsupportedPlatform = errors.New("客体不提供该设施")

func accessOK(rc *toolcompat.RunContext, operand string, op byte) (bool, error) {
	fi, err := vfs.Stat(operand)
	if err != nil {
		return false, nil
	}
	perm := fi.Mode().Perm()
	var bit fs.FileMode
	switch op {
	case 'r':
		bit = 0o400
	case 'w':
		bit = 0o200
	default:
		bit = 0o100
	}
	return perm&bit != 0, nil
}

func ownedByEffective(_ *toolcompat.RunContext, _ string, _ bool) (bool, error) {
	return false, errUnsupportedPlatform
}

func fileTimes(_ *toolcompat.RunContext, _ string) (atime, mtime int64, err error) {
	return 0, 0, errUnsupportedPlatform
}
