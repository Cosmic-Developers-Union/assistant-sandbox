package common

import "encoding/binary"

const timespecSize = 16

func encodeTimespec(seconds, nanoseconds int64) [timespecSize]byte {
	var encoded [timespecSize]byte
	binary.LittleEndian.PutUint64(encoded[0:], uint64(seconds))
	binary.LittleEndian.PutUint64(encoded[8:], uint64(nanoseconds))
	return encoded
}
