package cli

import (
	"testing"
	"time"
)

func TestLastDaysStart(t *testing.T) {
	now := time.Date(2024, 3, 10, 15, 4, 5, 0, time.Local)

	cases := []struct {
		last int
		want time.Time
	}{
		{1, time.Date(2024, 3, 10, 0, 0, 0, 0, time.Local)},
		{7, time.Date(2024, 3, 4, 0, 0, 0, 0, time.Local)},
		{30, time.Date(2024, 2, 10, 0, 0, 0, 0, time.Local)},
	}
	for _, c := range cases {
		got, err := lastDaysStart(c.last, now)
		if err != nil {
			t.Fatalf("--last %d: unexpected error: %v", c.last, err)
		}
		if got != c.want.Unix() {
			t.Errorf("--last %d: got %s, want %s", c.last, time.Unix(got, 0), c.want)
		}
	}

	if _, err := lastDaysStart(0, now); err == nil {
		t.Error("--last 0: expected an error")
	}
	if _, err := lastDaysStart(-3, now); err == nil {
		t.Error("--last -3: expected an error")
	}
}

func TestToFilterLast(t *testing.T) {
	ff := filterFlags{last: 3}
	f, err := ff.toFilter()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, _ := lastDaysStart(3, time.Now())
	if f.From != want {
		t.Errorf("From = %d, want %d", f.From, want)
	}
	if f.To != 0 {
		t.Errorf("To = %d, want 0 (open ended)", f.To)
	}

	for _, ff := range []filterFlags{{last: 7, from: "2024-01-01"}, {last: 7, to: "2024-12-31"}} {
		if _, err := ff.toFilter(); err == nil {
			t.Errorf("%+v: expected --last to conflict with --from/--to", ff)
		}
	}
}
