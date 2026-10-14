//go:build !windows

package pycore

import (
	"io"
	"os/exec"
)

func configure(*exec.Cmd) {}

// Outside Windows the Python core ends when its stdin closes with the Go core.
func bind(*exec.Cmd) (io.Closer, error) { return nil, nil }
