//go:build !windows && !linux && !darwin

package service

import (
	"context"
	"os"
	"path/filepath"
)

func SystemDir() string { return filepath.Join(os.TempDir(), "nyatunnel") }

func prepareDir() error                            { return ErrUnsupported }
func install(string) error                         { return ErrUnsupported }
func uninstall() error                             { return ErrUnsupported }
func status() (Status, error)                      { return Status{}, nil }
func IsService() bool                              { return false }
func RunService(func(context.Context) error) error { return ErrUnsupported }
