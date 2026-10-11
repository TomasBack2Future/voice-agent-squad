package dispatch

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
)

func legacyProcessArguments(pid int) ([]string, error) {
	file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, errors.New("native kernel argument identity unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return nil, errors.New("bounded native kernel argument identity unavailable")
	}
	fields := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	args := make([]string, len(fields))
	for i, field := range fields {
		args[i] = string(field)
	}
	return args, nil
}
