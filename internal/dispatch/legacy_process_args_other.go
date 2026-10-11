//go:build !darwin && !linux

package dispatch

import "errors"

func legacyProcessArguments(int) ([]string, error) {
	return nil, errors.New("this host has no qualified legacy native kernel observer")
}

func legacyProcessExecutable(int) (string, error) {
	return "", errors.New("this host has no qualified legacy native kernel observer")
}
