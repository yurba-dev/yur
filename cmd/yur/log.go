package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"yurba.one/yur"
)

// History

func (r *Repo) decorations() map[yur.Hash][]string {
	marks := map[yur.Hash][]string{}
	for _, name := range r.State.branchNames() {
		h := r.State.Branches[name].Head
		marks[h] = append(marks[h], paint(cyan, name))
	}
	if r.State.Remote != nil {
		for name, h := range r.State.Remote.Branches {
			marks[h] = append(marks[h], paint(blue, "remote/"+name))
		}
	}
	return marks
}

func (r *Repo) printRev(h yur.Hash, rev yur.Rev, marks map[yur.Hash][]string, full bool) {
	dot := paint(bold, "●")
	if len(rev.Parents) > 1 {
		dot = paint(purple, "◆")
	}
	author := rev.Author.Name
	if author == "" {
		author = "someone"
	}
	line := fmt.Sprintf("%s  %s %s  %s · %s", dot, paint(purple, rev.Change[:min(8, len(rev.Change))]), paint(dim, h.Short()), author, paint(dim, ago(rev.Time)))
	if names := marks[h]; len(names) > 0 {
		line += "  " + strings.Join(names, " ")
	}
	if len(rev.Conflicts) > 0 {
		line += "  " + paint(red, "conflicts")
	}
	fmt.Println(line)
	message := rev.Title()
	if full {
		message = strings.TrimSpace(rev.Message)
	}
	for _, l := range strings.Split(message, "\n") {
		fmt.Println("│  " + l)
	}
}

func cmdLog(r *Repo, a *args) error {
	b := r.State.current()
	limit, _ := strconv.Atoi(a.get("n"))
	if limit <= 0 {
		limit = 20
	}
	heads := []yur.Hash{b.Head}
	if a.bool("all") {
		for _, br := range r.State.Branches {
			heads = append(heads, br.Head)
		}
	}
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	if !b.Work.IsZero() && len(paths) == 0 {
		fmt.Printf("%s  %s  %s\n", paint(green, "@"), paint(purple, r.workChange(b)[:8]), paint(dim, "unsaved changes (yur save keeps them as a rev)"))
		fmt.Println("│")
	}
	walk := limit
	if len(paths) > 0 {
		walk = 0
	}
	order, revs, err := yur.Ancestors(r.Store, heads, nil, walk)
	if err != nil {
		return err
	}
	marks := r.decorations()
	shown := 0
	for _, h := range order {
		if shown >= limit {
			break
		}
		rev := revs[h]
		if len(paths) > 0 && !r.touches(rev, paths) {
			continue
		}
		r.printRev(h, rev, marks, false)
		shown++
	}
	if len(order) == 0 {
		fmt.Println(paint(dim, "No revs yet: yur save -m \"...\" makes the first"))
	}
	return nil
}

func (r *Repo) touches(rev yur.Rev, paths []string) bool {
	before := yur.EmptyTree(r.Store)
	if len(rev.Parents) > 0 {
		before = r.revTree(rev.Parents[0])
	}
	for _, p := range paths {
		x, errX := yur.Lookup(r.Store, before, p)
		y, errY := yur.Lookup(r.Store, rev.Tree, p)
		if (errX == nil) != (errY == nil) || x.Hash != y.Hash {
			return true
		}
	}
	return false
}

func cmdShow(r *Repo, a *args) error {
	name := a.arg(0)
	if name == "" {
		name = "@"
	}
	h, err := r.resolve(name)
	if err != nil {
		return err
	}
	if h.IsZero() {
		return errors.New("no revs yet")
	}
	rev, err := yur.ReadRev(r.Store, h)
	if err != nil {
		return err
	}
	r.printRev(h, rev, r.decorations(), true)
	fmt.Println()
	return r.printDiff(r.parentTree(h), rev.Tree, nil, a.bool("stat"))
}
