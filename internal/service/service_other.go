//go:build !windows && !linux && !darwin

package service

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

func SystemDir() string { return filepath.Join(os.TempDir(), "nyatunnel") }

func prepareDir() error                            { return ErrUnsupported }
func install(string) error                         { return ErrUnsupported }
func uninstall() error                             { return ErrUnsupported }
func status() (Status, error)                      { return Status{}, nil }
func IsService() bool                              { return false }
func RunService(func(context.Context) error) error { return ErrUnsupported }

// The service runs the binary it was installed from; automatic service updates are Windows-only.

func BinDir() string                         { return filepath.Join(SystemDir(), "bin") }
func placeBinary(exe string) (string, error) { return exe, nil }
func Stop(time.Duration) error               { return ErrUnsupported }
func Start() error                           { return ErrUnsupported }
func Executable() (string, error)            { return "", ErrUnsupported }
func SetExecutable(string) error             { return ErrUnsupported }
