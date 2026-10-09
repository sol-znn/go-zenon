//go:build !unix

package main

import "os/exec"

// ownGroup is the lab image's concern (proc_unix.go). Built for anything else,
// labd is being worked on, and stops a run by its first process as it did.
func ownGroup(*exec.Cmd) {}
