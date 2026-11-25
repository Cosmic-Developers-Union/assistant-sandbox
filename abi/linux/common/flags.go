package common

import "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"

type OpenMode uint8

const (
	OpenRead OpenMode = iota

	OpenWrite

	OpenReadWrite
)

type OpenFlags struct {
	Mode        OpenMode
	Create      bool
	Exclusive   bool
	Truncate    bool
	Append      bool
	CloseOnExec bool
	NoFollow    bool
	NonBlock    bool
	PathOnly    bool
}

func (f OpenFlags) Validate() error {
	if f.Mode > OpenReadWrite {
		return errno.Wrap(errno.Invalid, "未知的打开模式")
	}

	if f.Exclusive && !f.Create {
		return errno.Wrap(errno.Invalid, "O_EXCL 必须与 O_CREAT 同时出现")
	}

	if f.Append && f.Mode == OpenRead {
		return errno.Wrap(errno.Invalid, "O_APPEND 要求可写")
	}
	return nil
}
