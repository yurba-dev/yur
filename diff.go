package yur

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

func IsBinary(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(head[:validPrefix(head)])
}

// A cut at 8000 bytes may split a character, which alone must not make a text file binary
func validPrefix(b []byte) int {
	n := len(b)
	for i := 0; i < 3 && n > 0 && !utf8.RuneStart(b[n-1]); i++ {
		n--
	}
	// b[n-1] starts the last character: keep it whole when all its bytes are there, else drop it
	if n > 0 && utf8.FullRune(b[n-1:]) {
		return len(b)
	}
	if n > 0 {
		n--
	}
	return n
}

// Lines with their endings kept, so a file without a final newline comes out as it went in
func SplitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type Op struct {
	// '=' kept, '-' only in a, '+' only in b
	Kind byte
	A, B int
}

// Past this many differences the middle of the files is shown as replaced whole: an exact answer
// would cost memory growing with its square
const maxEdits = 1000

func DiffLines(a, b []string) []Op {
	ids := map[string]int{}
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = len(ids)
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	x, y := intern(a), intern(b)

	ops := []Op{}
	pre := 0
	for pre < len(x) && pre < len(y) && x[pre] == y[pre] {
		ops = append(ops, Op{'=', pre, pre})
		pre++
	}
	suf := 0
	for suf < len(x)-pre && suf < len(y)-pre && x[len(x)-1-suf] == y[len(y)-1-suf] {
		suf++
	}
	ops = append(ops, myers(x[pre:len(x)-suf], y[pre:len(y)-suf], pre, pre)...)
	for i := suf; i > 0; i-- {
		ops = append(ops, Op{'=', len(x) - i, len(y) - i})
	}
	return ops
}

func myers(x, y []int, offA, offB int) []Op {
	n, m := len(x), len(y)
	whole := func() []Op {
		ops := []Op{}
		for i := 0; i < n; i++ {
			ops = append(ops, Op{'-', offA + i, -1})
		}
		for j := 0; j < m; j++ {
			ops = append(ops, Op{'+', -1, offB + j})
		}
		return ops
	}
	if n == 0 || m == 0 {
		return whole()
	}
	max := n + m
	if max > 2*maxEdits {
		max = 2 * maxEdits
	}
	v := make([]int, 2*max+2)
	// Each round keeps only the part of v it can reach, so the trace grows with d squared and not with d times n
	trace := [][]int{}
	lows := []int{}
	found := -1
	for d := 0; d <= max && found < 0; d++ {
		low, high := max-d-1, max+d+2
		if low < 0 {
			low = 0
		}
		if high > len(v) {
			high = len(v)
		}
		trace = append(trace, append([]int(nil), v[low:high]...))
		lows = append(lows, low)
		for k := -d; k <= d; k += 2 {
			var i int
			if k == -d || (k != d && v[max+k-1] < v[max+k+1]) {
				i = v[max+k+1]
			} else {
				i = v[max+k-1] + 1
			}
			j := i - k
			for i < n && j < m && x[i] == y[j] {
				i++
				j++
			}
			v[max+k] = i
			if i >= n && j >= m {
				found = d
				break
			}
		}
	}
	if found < 0 {
		return whole()
	}

	ops := []Op{}
	i, j := n, m
	for d := found; d > 0; d-- {
		pv, low := trace[d], lows[d]
		at := func(k int) int { return pv[max+k-low] }
		k := i - j
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		pi := at(pk)
		pj := pi - pk
		for i > pi && j > pj {
			i--
			j--
			ops = append(ops, Op{'=', offA + i, offB + j})
		}
		if i == pi {
			j--
			ops = append(ops, Op{'+', -1, offB + j})
		} else {
			i--
			ops = append(ops, Op{'-', offA + i, -1})
		}
	}
	for i > 0 && j > 0 {
		i--
		j--
		ops = append(ops, Op{'=', offA + i, offB + j})
	}
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

type Line struct {
	Kind string
	// Numbers from 1; zero on the side the line isn't on
	Old  int
	New  int
	Text string
}

type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []Line
}

func Hunks(a, b []string, context int) (hunks []Hunk, added int, removed int) {
	ops := DiffLines(a, b)
	changed := []int{}
	for i, op := range ops {
		if op.Kind != '=' {
			changed = append(changed, i)
			if op.Kind == '+' {
				added++
			} else {
				removed++
			}
		}
	}
	for c := 0; c < len(changed); {
		start := changed[c] - context
		if start < 0 {
			start = 0
		}
		end := changed[c]
		for c < len(changed) && changed[c]-end <= 2*context+1 {
			end = changed[c]
			c++
		}
		end += context + 1
		if end > len(ops) {
			end = len(ops)
		}
		h := Hunk{}
		// A side with no lines in the hunk is placed after the line before it, as unified diffs do
		for _, op := range ops[:start] {
			if op.A >= 0 {
				h.OldStart = op.A + 1
			}
			if op.B >= 0 {
				h.NewStart = op.B + 1
			}
		}
		oldBefore, newBefore := h.OldStart, h.NewStart
		h.OldStart, h.NewStart = 0, 0
		for _, op := range ops[start:end] {
			line := Line{Kind: string(op.Kind)}
			if op.A >= 0 {
				line.Old = op.A + 1
				line.Text = a[op.A]
				h.OldLines++
				if h.OldStart == 0 {
					h.OldStart = line.Old
				}
			}
			if op.B >= 0 {
				line.New = op.B + 1
				line.Text = b[op.B]
				h.NewLines++
				if h.NewStart == 0 {
					h.NewStart = line.New
				}
			}
			line.Text = strings.TrimRight(line.Text, "\r\n")
			h.Lines = append(h.Lines, line)
		}
		if h.OldLines == 0 {
			h.OldStart = oldBefore
		}
		if h.NewLines == 0 {
			h.NewStart = newBefore
		}
		hunks = append(hunks, h)
	}
	return hunks, added, removed
}

func Unified(hunks []Hunk) string {
	var b strings.Builder
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			kind := l.Kind
			if kind == "=" {
				kind = " "
			}
			b.WriteString(kind + l.Text + "\n")
		}
	}
	return b.String()
}
