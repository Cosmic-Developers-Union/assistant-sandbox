package joincmd

import "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"

type ctypeProvider interface {
	ToUpper([]byte) ([]byte, error)
	Close() error
}

type ctypeOpener func(string) (ctypeProvider, error)

func openCType(name string) (ctypeProvider, error) { return locale.Open(name) }

func snapshotFoldTable(p ctypeProvider) (*[256]byte, error) {
	var t [256]byte
	for c := 0; c < 256; c++ {
		up, err := p.ToUpper([]byte{byte(c)})
		if err != nil {
			return nil, err
		}
		if len(up) == 1 {
			t[c] = up[0]
		} else {
			t[c] = byte(c)
		}
	}
	return &t, nil
}
