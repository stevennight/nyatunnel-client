//go:build !windows && !darwin

package update

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

var errUnsupported = errors.New("unattended updates are only implemented on Windows and macOS")

func (u GUIUpdate) Run(context.Context, *slog.Logger) Result {
	return Result{From: u.From, To: u.To, Error: errUnsupported.Error(), At: time.Now()}
}

func (u ServiceUpdate) Run(context.Context, *slog.Logger) Result {
	return Result{From: u.From, To: u.To, Error: errUnsupported.Error(), At: time.Now()}
}

// StartServiceUpdate is Windows/macOS-only; elsewhere services are updated with `nyatunnel update`.
func StartServiceUpdate(context.Context, bool) (string, error) { return "", nil }
