// Package proc runs external programs (ffmpeg, ffprobe, rclone) without flashing console windows.
package proc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// Result is what a finished program left behind.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run executes a program with an optional stdin and a time limit. A non-zero exit is not an error: look at
// Result.ExitCode. Errors are for programs that cannot start or that time out.
func Run(timeout time.Duration, stdin []byte, name string, args ...string) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	Hide(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		// Not the default message: it quotes the whole command line, presigned URL included.
		return nil, fmt.Errorf("%s timed out after %.0fs", filepath.Base(name), timeout.Seconds())
	}
	res := &Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return nil, err
	}
	return res, nil
}
