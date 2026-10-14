//go:build !windows

package stream

import (
	"context"
	"errors"
	"io"
)

func probeLoopback() ([]string, error) { return nil, errors.New("WASAPI is Windows-only") }

func captureLoopback(context.Context, io.Writer) error { return errors.New("WASAPI is Windows-only") }
