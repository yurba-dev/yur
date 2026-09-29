package yur

import (
	"errors"
)

// For each line of a file at head, the rev that brought it in: owners[i] indexes revs, -1 when the
// history ran past the steps allowed. Follows first parents, as the history of a branch reads.
func Blame(s Store, head Hash, file string, maxSteps int) (lines []string, owners []int, revs []Hash, err error) {
	rev, err := ReadRev(s, head)
	if err != nil {
		return nil, nil, nil, err
	}
	entry, err := Lookup(s, rev.Tree, file)
	if err != nil {
		return nil, nil, nil, errors.New("path_not_found")
	}
	data, err := ReadFile(s, entry.Hash)
	if err != nil {
		return nil, nil, nil, err
	}
	if IsBinary(data) {
		return nil, nil, nil, errors.New("not_text")
	}
	lines = SplitLines(data)
	owners = make([]int, len(lines))
	for i := range owners {
		owners[i] = -1
	}
	// where[i] is the line of the file at head that line i of the version being looked at became
	where := make([]int, len(lines))
	for i := range where {
		where[i] = i
	}
	left := len(lines)
	h, cur, content, fileHash := head, rev, lines, entry.Hash
	for steps := 0; left > 0 && steps < maxSteps; steps++ {
		var parent Hash
		var parentRev Rev
		var parentEntry Entry
		found := false
		if len(cur.Parents) > 0 {
			parent = cur.Parents[0]
			r, err := ReadRev(s, parent)
			if err != nil {
				// History that isn't here (a light clone offline): the lines left stay unknown
				break
			}
			parentRev = r
			if pe, err := Lookup(s, r.Tree, file); err == nil {
				parentEntry, found = pe, true
			}
		}
		// A rev that left the file as it was hands it on unchanged
		if found && parentEntry.Hash == fileHash {
			h, cur = parent, parentRev
			continue
		}
		var parentLines []string
		if found {
			d, err := ReadFile(s, parentEntry.Hash)
			if err != nil {
				break
			}
			parentLines = SplitLines(d)
		}
		index := len(revs)
		revs = append(revs, h)
		next := make([]int, len(parentLines))
		for i := range next {
			next[i] = -1
		}
		if found {
			for _, op := range DiffLines(parentLines, content) {
				if op.Kind == '=' {
					next[op.A] = where[op.B]
				}
			}
		}
		kept := map[int]bool{}
		for _, w := range next {
			if w >= 0 {
				kept[w] = true
			}
		}
		for _, w := range where {
			if w >= 0 && !kept[w] && owners[w] < 0 {
				owners[w] = index
				left--
			}
		}
		if !found {
			break
		}
		h, cur, content, where, fileHash = parent, parentRev, parentLines, next, parentEntry.Hash
	}
	return lines, owners, revs, nil
}
