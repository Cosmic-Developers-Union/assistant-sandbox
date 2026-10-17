package interp

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

type OpenInputFunc func(name string) (io.ReadCloser, error)

func DenyHostOpenFile(name string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("客体 AWK 不支持文件重定向")}
}
