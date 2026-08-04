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
}

func newProgress(w io.Writer, label string) *progress {
	return &progress{w: w, tty: isTTY(w), label: label}
}

func (p *progress) report(done, total int64) {
	if !p.tty {
		return
	}
	var line string
	if total > 0 {
		pct := float64(done) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
		line = fmt.Sprintf("%s %d/%d (%.1f%%)", p.label, done, total, pct)
	} else {
		line = fmt.Sprintf("%s %d", p.label, done)
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
