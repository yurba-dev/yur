package yur

import (
	"path"
	"sort"
	"strings"
)

// A stretch of the base one side replaced with lines of its own
type region struct {
	start, end int
	lines      []string
	side       int
}

func regions(base, other []string, side int) []region {
	out := []region{}
	var cur *region
	pos := 0
	for _, op := range DiffLines(base, other) {
		switch op.Kind {
		case '=':
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			pos = op.A + 1
		case '-':
			if cur == nil {
				cur = &region{start: op.A, end: op.A, side: side}
			}
			cur.end = op.A + 1
			pos = op.A + 1
		case '+':
			if cur == nil {
				cur = &region{start: pos, end: pos, side: side}
			}
			cur.lines = append(cur.lines, other[op.B])
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// Three-way merge of lines. Changes of one side go in as they are; where both sides changed the same
// place differently the result has both between markers, and ok is false.
func Merge3(base, ours, theirs []string, ourName, theirName string) (merged []string, ok bool) {
	all := append(regions(base, ours, 0), regions(base, theirs, 1)...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].start != all[j].start {
			return all[i].start < all[j].start
		}
		return all[i].side < all[j].side
	})

	ok = true
	pos := 0
	for i := 0; i < len(all); {
		start, end, endSide := all[i].start, all[i].end, all[i].side
		group := []region{all[i]}
		i++
		// Touching changes of the two sides are taken as one place, as a reader would: touching the
		// region that reaches furthest, whichever of the group came last
		for i < len(all) && (all[i].start < end || (all[i].start == end && all[i].side != endSide)) {
			if all[i].end > end {
				end, endSide = all[i].end, all[i].side
			}
			group = append(group, all[i])
			i++
		}
		merged = append(merged, base[pos:start]...)
		pos = end

		apply := func(side int) []string {
			out := []string{}
			at := start
			for _, r := range group {
				if r.side != side {
					continue
				}
				out = append(out, base[at:r.start]...)
				out = append(out, r.lines...)
				at = r.end
			}
			return append(out, base[at:end]...)
		}
		sides := map[int]bool{}
		for _, r := range group {
			sides[r.side] = true
		}
		switch {
		case !sides[1]:
			merged = append(merged, apply(0)...)
		case !sides[0]:
			merged = append(merged, apply(1)...)
		default:
			a, b := apply(0), apply(1)
			if strings.Join(a, "") == strings.Join(b, "") {
				merged = append(merged, a...)
				continue
			}
			ok = false
			merged = append(merged, "<<<<<<< "+ourName+"\n")
			merged = append(merged, endLines(a)...)
			merged = append(merged, "=======\n")
			merged = append(merged, endLines(b)...)
			merged = append(merged, ">>>>>>> "+theirName+"\n")
		}
	}
	merged = append(merged, base[pos:]...)
	return merged, ok
}

// Inside markers every line must end, or the marker after it would join it
func endLines(lines []string) []string {
	out := append([]string(nil), lines...)
	if n := len(out); n > 0 && !strings.HasSuffix(out[n-1], "\n") {
		out[n-1] += "\n"
	}
	return out
}

func HasMarkers(data []byte) bool {
	text := string(data)
	return strings.Contains(text, "<<<<<<< ") && strings.Contains(text, "\n=======\n") && strings.Contains(text, ">>>>>>> ")
}

// Merges trees: ours is the result's ground, theirs' changes since base are laid over it. Paths both
// changed are merged line by line; the ones that can't be are listed as conflicts, with markers in them.
func MergeTrees(s Store, base, ours, theirs Hash, ourName, theirName string) (Hash, []string, error) {
	if base == theirs || ours == theirs {
		return ours, nil, nil
	}
	if base == ours {
		return theirs, nil, nil
	}
	mine, err := DiffTrees(s, base, ours)
	if err != nil {
		return Zero, nil, err
	}
	their, err := DiffTrees(s, base, theirs)
	if err != nil {
		return Zero, nil, err
	}
	changed := map[string]FileChange{}
	// Folders ours now has files in, and files ours now has where a folder may be on theirs
	ourDirs := map[string]bool{}
	for _, c := range mine {
		changed[c.Path] = c
		if c.New != nil {
			for p := path.Dir(c.Path); p != "."; p = path.Dir(p) {
				ourDirs[p] = true
			}
		}
	}
	ourFileAbove := func(p string) bool {
		for q := path.Dir(p); q != "."; q = path.Dir(q) {
			if c, ok := changed[q]; ok && c.New != nil {
				return true
			}
		}
		return false
	}

	edits := map[string]*Entry{}
	conflicts := []string{}
	for _, t := range their {
		// A folder on one side where the other has a file: ours stays, and the place is shown as a conflict
		if ourFileAbove(t.Path) || t.New != nil && ourDirs[t.Path] {
			conflicts = append(conflicts, t.Path)
			continue
		}
		o, both := changed[t.Path]
		if !both {
			edits[t.Path] = t.New
			continue
		}
		if sameEntry(o.New, t.New) {
			continue
		}
		// Removed on one side and changed on the other: the changed file is kept, as a conflict to look at
		if o.New == nil || t.New == nil {
			if t.New != nil {
				edits[t.Path] = t.New
			}
			conflicts = append(conflicts, t.Path)
			continue
		}
		entry, clean, err := mergeFile(s, t.Old, o.New, t.New, ourName, theirName)
		if err != nil {
			return Zero, nil, err
		}
		if entry != nil {
			edits[t.Path] = entry
		}
		if !clean {
			conflicts = append(conflicts, t.Path)
		}
	}
	h, err := Edit(s, ours, edits)
	sort.Strings(conflicts)
	return h, conflicts, err
}

// One file both sides changed, line by line. A binary one can't be: ours is kept (entry nil) and it is
// a conflict. Of the kinds (file, program), the side that changed it wins.
func mergeFile(s Store, base, ours, theirs *Entry, ourName, theirName string) (*Entry, bool, error) {
	var baseData []byte
	var err error
	if base != nil {
		if baseData, err = ReadFile(s, base.Hash); err != nil {
			return nil, false, err
		}
	}
	ourData, err := ReadFile(s, ours.Hash)
	if err != nil {
		return nil, false, err
	}
	theirData, err := ReadFile(s, theirs.Hash)
	if err != nil {
		return nil, false, err
	}
	if IsBinary(ourData) || IsBinary(theirData) || IsBinary(baseData) {
		return nil, false, nil
	}
	lines, clean := Merge3(SplitLines(baseData), SplitLines(ourData), SplitLines(theirData), ourName, theirName)
	data := []byte(strings.Join(lines, ""))
	h, err := WriteFile(s, data)
	if err != nil {
		return nil, false, err
	}
	kind := ours.Kind
	if base != nil && kind == base.Kind {
		kind = theirs.Kind
	}
	return &Entry{Kind: kind, Hash: h, Size: int64(len(data))}, clean, nil
}

func sameEntry(a, b *Entry) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Hash == b.Hash && a.Kind == b.Kind
}
