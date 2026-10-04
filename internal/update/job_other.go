//go:build !windows

package update

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

var errWindowsOnly = errors.New("unattended updates are only implemented on Windows")

func (u GUIUpdate) Run(context.Context, *slog.Logger) Result {
	return Result{From: u.From, To: u.To, Error: errWindowsOnly.Error(), At: time.Now()}
}

func (u ServiceUpdate) Run(context.Context, *slog.Logger) Result {
	return Result{From: u.From, To: u.To, Error: errWindowsOnly.Error(), At: time.Now()}
}

// StartServiceUpdate is Windows-only; elsewhere services are updated with `nyatunnel update`.
func StartServiceUpdate(context.Context, bool) (string, error) { return "", nil }
