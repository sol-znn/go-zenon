//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup makes stopping a run stop everything the run started. A scenario
// is rarely one process: nom-to-nom is bash running znn-cli, and flows runs
// nom-to-btc for the two Bitcoin scenarios. Killing only the first leaves the
// rest signing as Alice and Bob after labd has told everyone the accounts are
// free. The run gets a process group of its own, and the group is what is
// killed.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
