package dispatch

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func legacyProcessArguments(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, errors.New("native kernel argument identity unavailable")
	}
	if len(raw) < 4 || len(raw) > 1<<20 {
		return nil, errors.New("native kernel argument identity exceeds its bound")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc < 1 || argc > 4096 {
		return nil, errors.New("native kernel argument count unavailable")
	}
	remaining := raw[4:]
	end := bytes.IndexByte(remaining, 0)
	if end < 0 {
		return nil, errors.New("native executable identity unavailable")
	}
	remaining = remaining[end+1:]
	for len(remaining) > 0 && remaining[0] == 0 {
		remaining = remaining[1:]
	}
	var args []string
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(remaining, 0)
		if end < 0 {
			return nil, errors.New("native argument record incomplete")
		}
		args = append(args, string(remaining[:end]))
		remaining = remaining[end+1:]
	}
	// Do not decode, return or persist the trailing environment.
	return args, nil
}

func legacyProcessExecutable(pid int) (string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 5 || len(raw) > 1<<20 {
		return "", errors.New("native kernel executable identity unavailable")
	}
	end := bytes.IndexByte(raw[4:], 0)
	if end < 1 {
		return "", errors.New("native kernel executable identity unavailable")
	}
	return string(raw[4 : 4+end]), nil
}
