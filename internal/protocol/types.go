package protocol

import "encoding/json/jsontext"

type Request struct {
	Message     string
	Messages    []jsontext.Value
	ID          uint64
	Command     string
	OutputLimit int64
}

type Response struct {
	ID               uint64
	Code             int
	Stdout, Stderr   string
	Closed, Overflow bool
}
