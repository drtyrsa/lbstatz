package cli

import (
	"bytes"
	"strings"
	"testing"
)

// newTestProgress forces the terminal path, which is the only one that draws anything.
func newTestProgress(label string) (*progress, *bytes.Buffer) {
	var buf bytes.Buffer
	return &progress{w: &buf, tty: true, label: label}, &buf
}

// lines splits what was written on the carriage returns that separate one redraw from the next.
func lines(buf *bytes.Buffer) []string {
	return strings.Split(strings.TrimPrefix(buf.String(), "\r"), "\r")
}

func TestProgressNoteRedrawsWithoutANewCount(t *testing.T) {
	p, buf := newTestProgress("Countries")
	p.report(30, 100)
	p.setNote("throttled by MusicBrainz, retrying in 8s")

	got := lines(buf)
	if len(got) != 2 {
		t.Fatalf("drew %d times (%q), want the note to force a redraw of its own", len(got), got)
	}
	if !strings.HasPrefix(got[1], "Countries 30/100 (30.0%)") {
		t.Errorf("noted line = %q, want the count kept alongside the note", got[1])
	}
	if !strings.Contains(got[1], "throttled by MusicBrainz, retrying in 8s") {
		t.Errorf("noted line = %q, want the note on it", got[1])
	}
}

// The note is set once per second during a wait; repeating it would flicker the line.
func TestProgressSkipsRedundantNotes(t *testing.T) {
	p, buf := newTestProgress("Countries")
	p.report(30, 100)
	p.setNote("waiting")
	p.setNote("waiting")

	if got := lines(buf); len(got) != 2 {
		t.Errorf("drew %d times (%q), want the repeat ignored", len(got), got)
	}
}

// Clearing the note has to cover the text it leaves behind, or its tail stays on screen.
func TestProgressClearedNoteLeavesNoTail(t *testing.T) {
	p, buf := newTestProgress("Countries")
	p.report(30, 100)
	p.setNote("throttled by MusicBrainz, retrying in 8s")
	buf.Reset()
	p.setNote("")

	got := buf.String()
	if strings.Contains(got, "throttled") {
		t.Fatalf("redraw = %q, still shows the note", got)
	}
	if !strings.HasSuffix(got, "   ") {
		t.Errorf("redraw = %q, want it padded over where the note was", got)
	}
}
