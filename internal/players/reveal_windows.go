//go:build windows

package players

import (
	"os/exec"
	"syscall"
)

// Reveal opens the folder that holds path in Explorer, with the file selected.
func Reveal(path string) error {
	cmd := exec.Command("explorer.exe")
	// Explorer reads its command line itself and wants /select,"<path>" as it is; Go's own quoting of an argument with
	// spaces would wrap the whole of it in quotes, which Explorer does not understand.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // Explorer exits with 1 even when it worked: its status says nothing
	return nil
}
