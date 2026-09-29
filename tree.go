package yur

import (
	"errors"
	"path"
	"sort"
	"strings"
)

// Paths are always forward-slashed and relative to the project root, whatever the system
func SplitPath(p string) []string {
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func CleanPath(p string) (string, bool) {
	parts := SplitPath(strings.ReplaceAll(p, "\\", "/"))
	for _, part := range parts {
		if !ValidName(part) {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
}

// The entry at a path; the root itself for an empty one
func Lookup(s Store, root Hash, p string) (Entry, error) {
	entry := Entry{Kind: KindDir, Hash: root}
	for _, name := range SplitPath(p) {
		if entry.Kind != KindDir {
			return Entry{}, errors.New("path_not_found")
		}
		tree, err := ReadTree(s, entry.Hash)
		if err != nil {
			return Entry{}, err
		}
		found, ok := tree.Find(name)
		if !ok {
			return Entry{}, errors.New("path_not_found")
		}
		entry = found
	}
	return entry, nil
}

// Every file of a tree by its path
func Flatten(s Store, root Hash) (map[string]Entry, error) {
	files := map[string]Entry{}
	var walk func(h Hash, prefix string) error
	walk = func(h Hash, prefix string) error {
		tree, err := ReadTree(s, h)
		if err != nil {
			return err
		}
		for _, e := range tree {
			if e.Kind == KindDir {
				if err := walk(e.Hash, prefix+e.Name+"/"); err != nil {
					return err
				}
				continue
			}
			files[prefix+e.Name] = e
		}
		return nil
	}
	return files, walk(root, "")
}

// Files are put in (or with nil taken out of) a tree; the folders on the way are made or dropped as needed
func Edit(s Store, root Hash, edits map[string]*Entry) (Hash, error) {
	tree, err := ReadTree(s, root)
	if err != nil {
		return Zero, err
	}
	here := map[string]*Entry{}
	below := map[string]map[string]*Entry{}
	for p, e := range edits {
		parts := SplitPath(p)
		if len(parts) == 0 {
			continue
		}
		if len(parts) == 1 {
			here[parts[0]] = e
			continue
		}
		if below[parts[0]] == nil {
			below[parts[0]] = map[string]*Entry{}
		}
		below[parts[0]][strings.Join(parts[1:], "/")] = e
	}

	byName := map[string]Entry{}
	for _, e := range tree {
		byName[e.Name] = e
	}
	for name, e := range here {
		if e == nil {
			delete(byName, name)
			continue
		}
		entry := *e
		entry.Name = name
		byName[name] = entry
	}
	for name, sub := range below {
		base := Zero
		if e, ok := byName[name]; ok && e.Kind == KindDir {
			base = e.Hash
		} else if ok && onlyRemovals(sub) {
			// Nothing is under a file to take out: the file itself stays
			continue
		}
		h, err := Edit(s, base, sub)
		if err != nil {
			return Zero, err
		}
		subTree, _ := ReadTree(s, h)
		if len(subTree) == 0 {
			delete(byName, name)
			continue
		}
		byName[name] = Entry{Kind: KindDir, Hash: h, Name: name}
	}

	out := Tree{}
	for _, e := range byName {
		out = append(out, e)
	}
	return WriteTree(s, out)
}

func onlyRemovals(edits map[string]*Entry) bool {
	for _, e := range edits {
		if e != nil {
			return false
		}
	}
	return true
}

type FileChange struct {
	Path string
	// Nil when the file was added (Old) or removed (New)
	Old *Entry
	New *Entry
}

// What differs between two trees; equal folders are skipped whole, which keeps it cheap on big projects
func DiffTrees(s Store, a, b Hash) ([]FileChange, error) {
	changes := []FileChange{}
	var walk func(a, b Hash, prefix string) error
	walk = func(a, b Hash, prefix string) error {
		if a == b {
			return nil
		}
		ta, err := ReadTree(s, a)
		if err != nil {
			return err
		}
		tb, err := ReadTree(s, b)
		if err != nil {
			return err
		}
		inA, inB := ta.byName(), tb.byName()
		sorted := make([]string, 0, len(inA)+len(inB))
		for name := range inA {
			sorted = append(sorted, name)
		}
		for name := range inB {
			if _, both := inA[name]; !both {
				sorted = append(sorted, name)
			}
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			ea, okA := inA[name]
			eb, okB := inB[name]
			if okA && okB && ea.Hash == eb.Hash && ea.Kind == eb.Kind {
				continue
			}
			dirA, dirB := okA && ea.Kind == KindDir, okB && eb.Kind == KindDir
			if dirA || dirB {
				subA, subB := Zero, Zero
				if dirA {
					subA = ea.Hash
				}
				if dirB {
					subB = eb.Hash
				}
				if err := walk(subA, subB, prefix+name+"/"); err != nil {
					return err
				}
			}
			change := FileChange{Path: prefix + name}
			if okA && !dirA {
				e := ea
				change.Old = &e
			}
			if okB && !dirB {
				e := eb
				change.New = &e
			}
			if change.Old != nil || change.New != nil {
				changes = append(changes, change)
			}
		}
		return nil
	}
	err := walk(a, b, "")
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, err
}

// A removed file and an added one with the same content are one file moved
type Rename struct {
	From, To string
}

func FindRenames(changes []FileChange) ([]FileChange, []Rename) {
	removed := map[Hash][]int{}
	for i, c := range changes {
		if c.New == nil && c.Old != nil {
			removed[c.Old.Hash] = append(removed[c.Old.Hash], i)
		}
	}
	drop := map[int]bool{}
	renames := []Rename{}
	for i, c := range changes {
		if c.Old != nil || c.New == nil {
			continue
		}
		list := removed[c.New.Hash]
		if len(list) == 0 {
			continue
		}
		// Of several, the one in the same folder, then the first
		pick := 0
		for k, j := range list {
			if path.Dir(changes[j].Path) == path.Dir(c.Path) {
				pick = k
				break
			}
		}
		j := list[pick]
		removed[c.New.Hash] = append(list[:pick], list[pick+1:]...)
		drop[i], drop[j] = true, true
		renames = append(renames, Rename{From: changes[j].Path, To: c.Path})
	}
	rest := []FileChange{}
	for i, c := range changes {
		if !drop[i] {
			rest = append(rest, c)
		}
	}
	return rest, renames
}
