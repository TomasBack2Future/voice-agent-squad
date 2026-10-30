//go:build ignore

// Fixture standing in for a finished Claude client: it spawns one helper child
// and leaves normally on SIGTERM unless told otherwise through its environment.
package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__idle" {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		<-sig
		if os.Getenv("FIXTURE_HELPER_IGNORES_TERM") == "1" {
			time.Sleep(time.Hour)
		}
		return
	}
	child := exec.Command(os.Args[0], "__idle", "--config", "/fixture/receiver-"+os.Getenv("FIXTURE_NATIVE")+"/config.json")
	child.Env = os.Environ()
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	if argv := os.Getenv("FIXTURE_CALLER"); argv != "" {
		caller := exec.Command("/bin/sh", "-c", argv)
		caller.Env = os.Environ()
		caller.Stdout, caller.Stderr = os.Stdout, os.Stderr
		if err := caller.Start(); err != nil {
			os.Exit(4)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	for {
		<-sig
		if os.Getenv("FIXTURE_IGNORE_TERM") == "1" {
			continue
		}
		if os.Getenv("FIXTURE_ORPHAN_HELPER") != "1" {
			_ = child.Process.Signal(syscall.SIGTERM)
			_ = child.Wait()
		}
		return
	}
}
