package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type progress struct {
	w       io.Writer
	tty     bool
	label   string
	lastLen int
	done    int64
	total   int64
	note    string
}

func newProgress(w io.Writer, label string) *progress {
	return &progress{w: w, tty: isTTY(w), label: label}
}

func (p *progress) report(done, total int64) {
	p.done, p.total = done, total
	p.draw()
}

// setNote appends a short status to the progress line, or clears it when msg is empty. The
// counter can sit still for a while — a throttling wait, a slow request — and the line has
// to say why rather than just look stuck, so this redraws straight away.
func (p *progress) setNote(msg string) {
	if msg == p.note {
		return
	}
	p.note = msg
	p.draw()
}

func (p *progress) draw() {
	if !p.tty {
		return
	}
	var line string
	if p.total > 0 {
		pct := float64(p.done) / float64(p.total) * 100
		if pct > 100 {
			pct = 100
		}
		line = fmt.Sprintf("%s %d/%d (%.1f%%)", p.label, p.done, p.total, pct)
	} else {
		line = fmt.Sprintf("%s %d", p.label, p.done)
	}
	if p.note != "" {
		line += " - " + p.note
	}
	pad := ""
	if len(line) < p.lastLen {
		pad = strings.Repeat(" ", p.lastLen-len(line))
	}
	fmt.Fprintf(p.w, "\r%s%s", line, pad)
	p.lastLen = len(line)
}

func (p *progress) finish(msg string) {
	p.clear()
	fmt.Fprintln(p.w, msg)
}

// clear erases the progress line without printing anything in its place, for phases that
// report only once they are all done.
func (p *progress) clear() {
	if p.tty && p.lastLen > 0 {
		fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.lastLen))
		p.lastLen = 0
	}
	p.note = ""
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
