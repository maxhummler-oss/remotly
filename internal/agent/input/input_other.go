//go:build !windows && !darwin && !linux

package input

import (
	"fmt"
	"runtime"
)

func newPlatform(bool) (platform, error) {
	return nil, fmt.Errorf("input injection is not supported on %s", runtime.GOOS)
}
