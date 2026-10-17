package commandtest

import (
	"context"
	"io"
	"io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func RunFault(t *testing.T, run commandio.Handler, dir string, args []string, input, operation string) Result {
	t.Helper()
	return Run(t, func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		system, err := p.Resources.Filesystem()
		if err != nil {
			return err
		}
		clock, _ := p.Resources.Time()
		descriptors, _ := p.Resources.Descriptors()
		random, _ := p.Resources.Random()
		set := capability.Set{FS: faultFS{system, operation}, FD: descriptors, Entropy: random, Clock: clock}
		if operation == "MissingFS" {
			set.FS = nil
		}
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}, dir, args, input)
}

type faultFS struct {
	vmfs.System
	operation string
}

func (f faultFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	if f.operation == "Open" || f.operation == "OpenWrite" && flags&(vmfs.WriteOnly|vmfs.ReadWrite) != 0 || f.operation == "OpenRead" && flags&(vmfs.WriteOnly|vmfs.ReadWrite) == 0 {
		return nil, fs.ErrPermission
	}
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	return faultFile{file, f.operation}, nil
}
func (f faultFS) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	if f.operation == "Stat" {
		return nil, fs.ErrPermission
	}
	return f.System.Stat(ctx, name)
}
func (f faultFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if f.operation == "ReadDir" {
		return nil, fs.ErrPermission
	}
	return f.System.ReadDir(ctx, name)
}
func (f faultFS) Mkdir(ctx context.Context, name string, mode fs.FileMode) error {
	if f.operation == "Mkdir" {
		return fs.ErrPermission
	}
	return f.System.Mkdir(ctx, name, mode)
}
func (f faultFS) Remove(ctx context.Context, name string) error {
	if f.operation == "Remove" {
		return fs.ErrPermission
	}
	return f.System.Remove(ctx, name)
}
func (f faultFS) Rename(ctx context.Context, from, to string) error {
	if f.operation == "Rename" {
		return fs.ErrPermission
	}
	return f.System.Rename(ctx, from, to)
}

type faultFile struct {
	vmfs.File
	operation string
}

func (f faultFile) Read(p []byte) (int, error) {
	if f.operation == "Read" {
		return 0, io.ErrUnexpectedEOF
	}
	return f.File.Read(p)
}
func (f faultFile) Write(p []byte) (int, error) {
	if f.operation == "Write" {
		return 0, io.ErrClosedPipe
	}
	return f.File.Write(p)
}
func (f faultFile) Close() error {
	err := f.File.Close()
	if f.operation == "Close" {
		return io.ErrClosedPipe
	}
	return err
}

func (f faultFS) SetTimes(ctx context.Context, name string, access, modification int64) error {
	if f.operation == "SetTimes" {
		return fs.ErrPermission
	}
	return f.System.(vmfs.Timestamps).SetTimes(ctx, name, access, modification)
}
