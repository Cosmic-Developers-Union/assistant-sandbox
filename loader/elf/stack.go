package elf

import (
	"encoding/binary"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

const (
	atNull   = 0
	atPhdr   = 3
	atPhent  = 4
	atPhnum  = 5
	atPagesz = 6
	atBase   = 7
	atFlags  = 8
	atEntry  = 9
	atUID    = 11
	atEUID   = 12
	atGID    = 13
	atEGID   = 14
	atHWCAP  = 16
	atClkTCK = 17
	atSecure = 23
	atRandom = 25
	atExecFN = 31
)

const clockTicksPerSecond = 100

const stackAlignment = 16

type auxFacts struct {
	entry            uint64
	programAddress   uint64
	programEntrySize uint64
	programCount     uint64
}

func buildStack(options Options, facts auxFacts) (Stack, error) {
	argv, env := options.Argv, options.Env

	blobs := make([]string, 0, len(argv)+len(env)+2)
	blobs = append(blobs, argv...)
	blobs = append(blobs, env...)
	blobs = append(blobs, string(options.Random[:]), options.ExecPath)
	addresses := make([]uint64, len(blobs))
	cursor := options.StackTop
	for index := len(blobs) - 1; index >= 0; index-- {
		size := uint64(len(blobs[index])) + 1
		start, ok := subOverflow(cursor, size)
		if !ok {
			return Stack{}, limit("初始栈装不下命令行与环境")
		}
		cursor = start
		addresses[index] = cursor
	}
	randomAddress := addresses[len(argv)+len(env)]
	execAddress := addresses[len(argv)+len(env)+1]

	entries := [][2]uint64{
		{atPhdr, facts.programAddress},
		{atPhent, facts.programEntrySize},
		{atPhnum, facts.programCount},
		{atPagesz, PageSize},
		{atBase, 0},
		{atFlags, 0},
		{atEntry, facts.entry},

		{atUID, 0}, {atEUID, 0}, {atGID, 0}, {atEGID, 0},
		{atHWCAP, 0},
		{atClkTCK, clockTicksPerSecond},
		{atSecure, 0},
		{atRandom, randomAddress},
		{atExecFN, execAddress},
		{atNull, 0},
	}
	blockBytes := uint64(len(argv)+1)*8 + uint64(len(env)+1)*8 + uint64(len(entries))*16 + 8
	cursor = alignDown(cursor, stackAlignment)
	if (len(argv)+len(env))%2 == 0 {

		cursor -= 8
	}
	blockStart, ok := subOverflow(cursor, blockBytes)
	if !ok {
		return Stack{}, limit("初始栈装不下命令行与环境")
	}

	regionStart := options.StackTop - options.StackSize
	contentBase := alignDown(blockStart, PageSize)
	if contentBase < regionStart {
		return Stack{}, limit(fmt.Sprintf("初始栈需要 %d 字节，超过栈大小 %d",
			options.StackTop-blockStart, options.StackSize))
	}

	content := make([]byte, options.StackTop-contentBase)
	for index, address := range addresses {
		copy(content[address-contentBase:], blobs[index])
	}

	at := func(address uint64) uint64 { return address - contentBase }
	position := blockStart
	binary.LittleEndian.PutUint64(content[at(position):], uint64(len(argv)))
	position += 8
	for index := range argv {
		binary.LittleEndian.PutUint64(content[at(position):], addresses[index])
		position += 8
	}
	binary.LittleEndian.PutUint64(content[at(position):], 0)
	position += 8
	for index := range env {
		binary.LittleEndian.PutUint64(content[at(position):], addresses[len(argv)+index])
		position += 8
	}
	binary.LittleEndian.PutUint64(content[at(position):], 0)
	position += 8
	for _, entry := range entries {
		binary.LittleEndian.PutUint64(content[at(position):], entry[0])
		position += 8
		binary.LittleEndian.PutUint64(content[at(position):], entry[1])
		position += 8
	}
	return Stack{
		Region:      memory.Region{Start: regionStart, End: options.StackTop, Prot: memory.ProtRead | memory.ProtWrite, Name: "stack"},
		SP:          blockStart,
		ContentBase: contentBase,
		Content:     content,
	}, nil
}
