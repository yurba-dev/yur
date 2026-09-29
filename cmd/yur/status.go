package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"yurba.one/yur"
)

// Status

type fileStat struct {
	change   yur.FileChange
	added    int
	removed  int
	binary   bool
	rename   string
	conflict bool
}

func (r *Repo) changes(from, to yur.Hash, paths []string) ([]fileStat, error) {
	list, err := yur.DiffTrees(r.Store, from, to)
	if err != nil {
		return nil, err
	}
	list = filterPaths(list, paths)
	rest, renames := yur.FindRenames(list)
	out := []fileStat{}
	for _, rn := range renames {
		out = append(out, fileStat{change: yur.FileChange{Path: rn.To}, rename: rn.From})
	}
	for _, c := range rest {
		st := fileStat{change: c}
		old, now := r.fileData(c.Old), r.fileData(c.New)
		if yur.IsBinary(old) || yur.IsBinary(now) {
			st.binary = true
		} else if len(old)+len(now) < 4<<20 {
			_, st.added, st.removed = yur.Hunks(yur.SplitLines(old), yur.SplitLines(now), 0)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].change.Path < out[j].change.Path })
	return out, nil
}

func (r *Repo) fileData(e *yur.Entry) []byte {
	if e == nil {
		return nil
	}
	data, _ := yur.ReadFile(r.Store, e.Hash)
	return data
}

func filterPaths(list []yur.FileChange, paths []string) []yur.FileChange {
	if len(paths) == 0 {
		return list
	}
	out := []yur.FileChange{}
	for _, c := range list {
		for _, p := range paths {
			if c.Path == p || strings.HasPrefix(c.Path, strings.TrimSuffix(p, "/")+"/") || p == "" {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// Paths as typed, from wherever in the project the command runs, as paths from its root
func (r *Repo) paths(list []string) ([]string, error) {
	out := []string{}
	cwd, _ := os.Getwd()
	for _, p := range list {
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(cwd, p)
		}
		rel, err := filepath.Rel(r.Root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside the project", p)
		}
		clean, ok := yur.CleanPath(filepath.ToSlash(rel))
		// Anything that doesn't clean (.yur/x) would match every path, and restore would take back all the work
		if !ok {
			return nil, fmt.Errorf("%s is not a path of the project's files", p)
		}
		out = append(out, clean)
	}
	return out, nil
}

func printStats(list []fileStat) {
	for _, st := range list {
		mark, color := "M", yellow
		switch {
		case st.rename != "":
			mark, color = "R", purple
		case st.change.Old == nil:
			mark, color = "A", green
		case st.change.New == nil:
			mark, color = "D", red
		}
		name := st.change.Path
		if st.rename != "" {
			name = st.rename + " → " + st.change.Path
		}
		counts := ""
		switch {
		case st.binary:
			counts = paint(dim, "binary")
		case st.added > 0 || st.removed > 0:
			counts = paint(green, "+"+strconv.Itoa(st.added)) + " " + paint(red, "-"+strconv.Itoa(st.removed))
		}
		fmt.Printf("  %s %s  %s\n", paint(color, mark), name, counts)
	}
}

// Conflicted files that still hold markers
func (r *Repo) openConflicts(b *Branch) []string {
	left := []string{}
	for _, p := range b.Conflicts {
		data, err := os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(p)))
		if err == nil && yur.HasMarkers(data) {
			left = append(left, p)
		}
	}
	return left
}

func (r *Repo) aheadBehind(local, remote yur.Hash) (int, int) {
	if local == remote {
		return 0, 0
	}
	ahead, behind, _ := yur.AheadBehind(r.Store, local, remote, 0)
	return ahead, behind
}

func cmdStatus(r *Repo, a *args) error {
	b := r.State.current()
	line := "On branch " + paint(cyan, r.State.Branch)
	if change := r.workChange(b); change != "" {
		line += paint(dim, " · working on change ") + paint(purple, change[:8])
	}
	fmt.Println(line)
	if r.State.Remote != nil {
		if remote, ok := r.State.Remote.Branches[r.State.Branch]; ok {
			ahead, behind := r.aheadBehind(b.Head, remote)
			switch {
			case ahead > 0 && behind > 0:
				fmt.Printf("%s and %s the server: yur sync puts yours on top of its\n", plural(ahead, "rev")+" ahead", plural(behind, "rev")+" behind")
			case ahead > 0:
				fmt.Printf("%s the server has not got: yur sync sends them\n", plural(ahead, "rev"))
			case behind > 0:
				fmt.Printf("%s behind the server: yur sync brings them\n", plural(behind, "rev"))
			default:
				fmt.Println(paint(dim, "Up to date with the server (as of the last sync)"))
			}
		} else {
			fmt.Println(paint(dim, "Not on the server yet: yur push sends the branch"))
		}
	}
	if b.Merge != nil && b.Merge.Parent != nil {
		fmt.Printf("Merging %s: yur save finishes it, yur merge --abort drops it\n", paint(cyan, b.Merge.Name))
	}

	list, err := r.changes(r.headTree(b), r.workTree(b), nil)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println(paint(dim, "No changes"))
	} else {
		fmt.Println("\nChanges:")
		printStats(list)
	}
	if left := r.openConflicts(b); len(left) > 0 {
		fmt.Println("\n" + paint(red, "Conflicts") + paint(dim, " (edit out the markers, or yur resolve --ours/--theirs, then yur save):"))
		for _, p := range left {
			fmt.Println("  " + paint(red, "!") + " " + p)
		}
	}
	return nil
}

// Diff

func cmdDiff(r *Repo, a *args) error {
	b := r.State.current()
	from, to := r.headTree(b), r.workTree(b)
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	if len(a.list) > 0 && strings.Contains(a.list[0], "..") {
		left, right, _ := strings.Cut(a.list[0], "..")
		x, err := r.resolve(left)
		if err != nil {
			return err
		}
		y, err := r.resolve(right)
		if err != nil {
			return err
		}
		from, to = r.revTree(x), r.revTree(y)
		paths = paths[1:]
	} else if rev := a.get("r", "rev"); rev != "" {
		h, err := r.resolve(rev)
		if err != nil {
			return err
		}
		from, to = r.parentTree(h), r.revTree(h)
	}
	return r.printDiff(from, to, paths, a.bool("stat"))
}

func (r *Repo) revTree(h yur.Hash) yur.Hash {
	if rev, err := yur.ReadRev(r.Store, h); err == nil {
		return rev.Tree
	}
	return yur.EmptyTree(r.Store)
}

func (r *Repo) parentTree(h yur.Hash) yur.Hash {
	if rev, err := yur.ReadRev(r.Store, h); err == nil && len(rev.Parents) > 0 {
		return r.revTree(rev.Parents[0])
	}
	return yur.EmptyTree(r.Store)
}

func (r *Repo) printDiff(from, to yur.Hash, paths []string, stat bool) error {
	list, err := r.changes(from, to, paths)
	if err != nil {
		return err
	}
	if stat || len(list) == 0 {
		if len(list) == 0 {
			fmt.Println(paint(dim, "No changes"))
		}
		printStats(list)
		return nil
	}
	for _, st := range list {
		c := st.change
		title := c.Path
		switch {
		case st.rename != "":
			title = st.rename + " → " + c.Path + paint(dim, " (moved)")
		case c.Old == nil:
			title += paint(green, " (new)")
		case c.New == nil:
			title += paint(red, " (removed)")
		}
		fmt.Println(paint(bold, title))
		if st.rename != "" {
			fmt.Println()
			continue
		}
		if st.binary {
			fmt.Println(paint(dim, "  binary file") + "\n")
			continue
		}
		hunks, _, _ := yur.Hunks(yur.SplitLines(r.fileData(c.Old)), yur.SplitLines(r.fileData(c.New)), 3)
		for _, h := range hunks {
			fmt.Println(paint(cyan, fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)))
			for _, l := range h.Lines {
				switch l.Kind {
				case "+":
					fmt.Println(paint(green, "+"+l.Text))
				case "-":
					fmt.Println(paint(red, "-"+l.Text))
				default:
					fmt.Println(" " + l.Text)
				}
			}
		}
		fmt.Println()
	}
	return nil
}
