// Package version carries build metadata injected with -ldflags.
package version

var (
	// Version is the semantic version, e.g. "v0.1.0". Overridden at release time.
	Version = "0.1.0-dev"
	// Commit is the git commit the binary was built from.
	Commit = ""
	// BuildDate is the UTC build timestamp in RFC 3339 format.
	BuildDate = ""
)

// String returns a single-line description suitable for --version output.
func String() string {
	s := Version
	if Commit != "" {
		short := Commit
		if len(short) > 12 {
			short = short[:12]
		}
		s += " (" + short + ")"
	}
	if BuildDate != "" {
		s += " built " + BuildDate
	}
	return s
}
