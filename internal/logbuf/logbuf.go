// Package logbuf keeps the most recent log lines in memory so the GUI can show them.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Line is one log entry.
type Line struct {
	Seq   int64     `json:"seq"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

// Buffer is a ring of recent lines.
type Buffer struct {
	mu    sync.Mutex
	lines []Line
	next  int64
	max   int
}

// New keeps up to max lines.
func New(max int) *Buffer { return &Buffer{max: max} }

func (b *Buffer) add(level, text string, t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	b.lines = append(b.lines, Line{Seq: b.next, Time: t, Level: level, Text: text})
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

// After returns lines with Seq > after.
func (b *Buffer) After(after int64) []Line {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Line{}
	for _, l := range b.lines {
		if l.Seq > after {
			out = append(out, l)
		}
	}
	return out
}

// Handler returns a slog handler that writes into the buffer and forwards to next (may be nil).
func (b *Buffer) Handler(next slog.Handler) slog.Handler { return &handler{b: b, next: next} }

type handler struct {
	b     *Buffer
	next  slog.Handler
	attrs []slog.Attr
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value.Any())
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	h.b.add(strings.ToLower(r.Level.String()), sb.String(), r.Time)
	if h.next != nil {
		return h.next.Handle(ctx, r)
	}
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var next slog.Handler
	if h.next != nil {
		next = h.next.WithAttrs(attrs)
	}
	return &handler{b: h.b, next: next, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h *handler) WithGroup(name string) slog.Handler { return h }
