//go:build darwin

package service

import (
	"strings"
	"testing"
)

func TestRenderAndParsePlist(t *testing.T) {
	exe := "/Library/Application Support/NyaTunnel/bin/nyatunnel"
	plist := renderPlist(exe, SystemDir())
	if !strings.Contains(plist, "<key>SuccessfulExit</key>") {
		t.Fatalf("plist should keep alive only on non-zero exit:\n%s", plist)
	}
	got, err := parsePlistExecutable([]byte(plist))
	if err != nil {
		t.Fatalf("parsePlistExecutable: %v", err)
	}
	if got != exe {
		t.Fatalf("got %q, want %q", got, exe)
	}
}

func TestParsePlistExecutableWithXMLChars(t *testing.T) {
	exe := "/Library/Application Support/Nya & Tunnel/nyatunnel"
	plist := renderPlist(exe, SystemDir())
	got, err := parsePlistExecutable([]byte(plist))
	if err != nil {
		t.Fatalf("parsePlistExecutable: %v", err)
	}
	if got != exe {
		t.Fatalf("got %q, want %q", got, exe)
	}
}

func TestParseLaunchctlPrint(t *testing.T) {
	runningOut := `
system/io.github.stevennight.nyatunnel = {
	active count = 1
	path = /Library/LaunchDaemons/io.github.stevennight.nyatunnel.plist
	state = running
	pid = 12345
}
`
	loaded, running, detail := parseLaunchctlPrint(runningOut)
	if !loaded || !running || detail != "running" {
		t.Fatalf("got loaded=%v running=%v detail=%q", loaded, running, detail)
	}

	stoppedOut := `
system/io.github.stevennight.nyatunnel = {
	active count = 0
	state = not running
}
`
	loaded, running, detail = parseLaunchctlPrint(stoppedOut)
	if !loaded || running || detail != "not running" {
		t.Fatalf("got loaded=%v running=%v detail=%q", loaded, running, detail)
	}
}
