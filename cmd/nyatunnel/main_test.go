package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	cases := []struct {
		args     []string
		code     int
		inStdout string
	}{
		{[]string{"version"}, 0, "nyatunnel "},
		{[]string{"link", "nyatunnel://enroll?v=1&s=tunnel.example.net&c=K7QP-3XMD"}, 0, "K7QP-3XMD"},
		{[]string{"link", "nyatunnel://enroll?v=9&s=tunnel.example.net&c=K7QP-3XMD"}, 1, ""},
		{[]string{"bogus"}, 2, ""},
		{nil, 2, ""},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if got := run(c.args, strings.NewReader(""), &out, &errOut); got != c.code {
			t.Errorf("run(%q) = %d, want %d (stderr %q)", c.args, got, c.code, errOut.String())
		}
		if !strings.Contains(out.String(), c.inStdout) {
			t.Errorf("run(%q) stdout %q, want it to contain %q", c.args, out.String(), c.inStdout)
		}
	}
}
