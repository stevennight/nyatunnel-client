//go:build windows

package service

import "testing"

func TestExeOfServiceCommandLines(t *testing.T) {
	for in, want := range map[string]string{
		`"C:\Program Files\NyaTunnel\nyatunnel.exe" service run`: `C:\Program Files\NyaTunnel\nyatunnel.exe`,
		`C:\ProgramData\NyaTunnel\bin\nyatunnel.exe service run`: `C:\ProgramData\NyaTunnel\bin\nyatunnel.exe`,
	} {
		if got := exeOf(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
