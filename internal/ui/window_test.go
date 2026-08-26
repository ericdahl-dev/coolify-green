package ui

import (
	"strconv"
	"strings"
	"testing"
)

func numberedLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "line" + strconv.Itoa(i)
	}
	return out
}

func TestWindowLinesReturnsEverythingWhenItFits(t *testing.T) {
	lines := numberedLines(5)
	if got := windowLines(lines, 2, 20); len(got) != 5 {
		t.Errorf("got %d lines, want 5", len(got))
	}
	// An unknown terminal size must not truncate.
	if got := windowLines(lines, 2, 0); len(got) != 5 {
		t.Errorf("unknown height truncated to %d lines", len(got))
	}
}

func TestWindowLinesKeepsCursorVisible(t *testing.T) {
	lines := numberedLines(100)
	for _, cursor := range []int{0, 1, 50, 98, 99} {
		got := windowLines(lines, cursor, 12)
		if len(got) > 12 {
			t.Fatalf("cursor %d: %d lines exceeds the budget", cursor, len(got))
		}
		want := lines[cursor]
		found := false
		for _, l := range got {
			if strings.Contains(l, want+" ") || l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("cursor %d: %q not in window %v", cursor, want, got)
		}
	}
}

func TestWindowLinesMarksWhatIsHidden(t *testing.T) {
	lines := numberedLines(100)
	got := windowLines(lines, 50, 12)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "↑ ") || !strings.Contains(joined, "↓ ") {
		t.Errorf("both overflow markers expected:\n%s", joined)
	}

	top := windowLines(lines, 0, 12)
	if strings.Contains(strings.Join(top, "\n"), "↑ ") {
		t.Errorf("no rows are above the top:\n%s", strings.Join(top, "\n"))
	}
	bottom := windowLines(lines, 99, 12)
	if strings.Contains(strings.Join(bottom, "\n"), "↓ ") {
		t.Errorf("no rows are below the end:\n%s", strings.Join(bottom, "\n"))
	}
}

func TestWindowLinesWithoutACursorShowsTheTop(t *testing.T) {
	got := windowLines(numberedLines(100), -1, 12)
	if !strings.Contains(strings.Join(got, "\n"), "line0") {
		t.Errorf("expected the first rows:\n%s", strings.Join(got, "\n"))
	}
}
