package ui

import (
	"testing"

	"llmnrf/internal/probe"
)

func TestWidthsFitTheTerminal(t *testing.T) {
	for _, w := range []int{11, 24, 40, 60, 80, 120, 240} {
		m := Model{width: w}
		cols := m.widths()
		if len(cols) != 7 {
			t.Fatalf("width %d: got %d columns", w, len(cols))
		}
		sum := 10 // cursor cell + one space after each column
		for _, c := range cols {
			if c < 0 {
				t.Errorf("width %d: negative column width %d", w, c)
			}
			sum += c
		}
		if sum > w {
			t.Errorf("width %d: columns total %d, overflows the terminal", w, sum)
		}
	}
}

// A narrow terminal must shrink every column below its wide-terminal width
// instead of letting values wrap onto the next line.
func TestNarrowTerminalShrinksColumns(t *testing.T) {
	wide := Model{width: 200}.widths()
	narrow := Model{width: 60}.widths()
	for i := range wide {
		if narrow[i] >= wide[i] {
			t.Errorf("column %d: narrow width %d not below wide width %d", i, narrow[i], wide[i])
		}
	}
}

func TestRowRenderNeverExceedsTerminal(t *testing.T) {
	long := &probe.Result{
		Engine:     "some very long engine identification that will never fit",
		Confidence: probe.TierConfirmed,
		Models: []string{
			"a-model-name-longer-than-any-column",
			"second-model",
			"third-model",
		},
		Context: 1000000,
		Quant:   "Q4_K_M",
	}
	r := row{addr: "fd7a:115c:a1e0::ec01:f6b3:65535", isLLM: true, result: long}
	for _, w := range []int{40, 60, 80, 120} {
		m := Model{width: w}
		line := r.render(">", m.widths())
		if n := len([]rune(line)); n > w {
			t.Errorf("width %d: rendered %d cells: %q", w, n, line)
		}
	}
}

// A listener with no probe data still renders.
func TestPlainListenerRowRenders(t *testing.T) {
	m := Model{width: 80}
	r := row{addr: "127.0.0.1:22"}
	line := r.render(" ", m.widths())
	if len([]rune(line)) > 80 {
		t.Errorf("rendered %d cells: %q", len([]rune(line)), line)
	}
}
