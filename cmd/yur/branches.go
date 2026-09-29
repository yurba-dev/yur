package main

import (
	"errors"
	"fmt"
	"time"

	"yurba.one/yur"
)

// Branches

func cmdBranch(r *Repo, a *args) error {
	if name := a.get("d", "delete"); name != "" {
		if name == r.State.Branch {
			return errors.New("you are on this branch: switch to another first")
		}
		if _, ok := r.State.Branches[name]; !ok {
			return errors.New("no branch " + name)
		}
		delete(r.State.Branches, name)
		fmt.Printf("Removed branch %s %s\n", name, paint(dim, "(yur undo brings it back)"))
		return nil
	}
	if old := a.get("m"); old != "" && len(a.list) == 1 {
		return renameBranch(r, old, a.list[0])
	}
	if a.bool("rename") && len(a.list) == 2 {
		return renameBranch(r, a.list[0], a.list[1])
	}
	name := a.arg(0)
	if name == "" {
		marks := r.State.branchNames()
		for _, n := range marks {
			br := r.State.Branches[n]
			prefix := "  "
			if n == r.State.Branch {
				prefix = paint(green, "* ")
			}
			title := ""
			if rev, err := yur.ReadRev(r.Store, br.Head); err == nil {
				title = paint(dim, br.Head.Short()) + " " + rev.Title()
			}
			extra := ""
			if !br.Work.IsZero() {
				extra = paint(yellow, " (unsaved changes)")
			}
			fmt.Printf("%s%s  %s%s\n", prefix, paint(cyan, n), title, extra)
		}
		if r.State.Remote != nil {
			for n := range r.State.Remote.Branches {
				if _, ok := r.State.Branches[n]; !ok {
					fmt.Printf("  %s\n", paint(blue, "remote/"+n))
				}
			}
		}
		return nil
	}
	if !yur.ValidRef(name) {
		return errors.New("a branch name is letters, digits and - _ . / only")
	}
	if _, ok := r.State.Branches[name]; ok {
		return errors.New("there is a branch " + name + " already")
	}
	from := r.State.current().Head
	if f := a.get("from"); f != "" {
		h, err := r.resolve(f)
		if err != nil {
			return err
		}
		from = h
	}
	r.State.Branches[name] = &Branch{Head: from}
	fmt.Printf("Made branch %s %s\n", paint(cyan, name), paint(dim, "(yur switch "+name+" goes there)"))
	return nil
}

func renameBranch(r *Repo, old, name string) error {
	br, ok := r.State.Branches[old]
	if !ok {
		return errors.New("no branch " + old)
	}
	if !yur.ValidRef(name) {
		return errors.New("a branch name is letters, digits and - _ . / only")
	}
	if _, taken := r.State.Branches[name]; taken {
		return errors.New("there is a branch " + name + " already")
	}
	delete(r.State.Branches, old)
	r.State.Branches[name] = br
	if r.State.Branch == old {
		r.State.Branch = name
	}
	fmt.Printf("Renamed %s to %s\n", old, paint(cyan, name))
	return nil
}

func cmdSwitch(r *Repo, a *args) error {
	name := a.arg(0)
	if name == "" {
		return errors.New("which branch? yur switch <branch>")
	}
	current := r.State.current()
	from := r.workTree(current)
	if a.bool("c", "create") {
		if _, ok := r.State.Branches[name]; ok {
			return errors.New("there is a branch " + name + " already")
		}
		if !yur.ValidRef(name) {
			return errors.New("a branch name is letters, digits and - _ . / only")
		}
		// The unsaved work goes along to the new branch: it was begun for it
		r.State.Branches[name] = &Branch{Head: current.Head, Work: current.Work}
		current.Work = yur.Zero
		r.State.Branch = name
		fmt.Printf("On the new branch %s\n", paint(cyan, name))
		return nil
	}
	if name == r.State.Branch {
		fmt.Println("On " + name + " already")
		return nil
	}
	if _, ok := r.State.Branches[name]; !ok {
		remote, ok := r.State.Remote.branch(name)
		if !ok {
			return errors.New("no branch " + name + " (yur switch -c " + name + " makes it)")
		}
		r.State.Branches[name] = &Branch{Head: remote}
	}
	r.State.Branch = name
	if err := r.showBranch(from); err != nil {
		return err
	}
	fmt.Printf("On branch %s\n", paint(cyan, name))
	if !current.Work.IsZero() {
		fmt.Println(paint(dim, "The unsaved changes stay on the branch you left, they come back with it."))
	}
	if !r.State.current().Work.IsZero() {
		fmt.Println(paint(dim, "This branch's own unsaved changes are back."))
	}
	return nil
}

func (rm *Remote) branch(name string) (yur.Hash, bool) {
	if rm == nil {
		return yur.Zero, false
	}
	h, ok := rm.Branches[name]
	return h, ok
}

// Merge

func (r *Repo) mergeInto(b *Branch, name string, other yur.Hash, saveMessage string) error {
	if other.IsZero() || yur.IsAncestor(r.Store, other, b.Head) {
		fmt.Println(paint(dim, "Nothing to merge: "+name+" has nothing this branch lacks"))
		return nil
	}
	work := r.workTree(b)
	if b.Head.IsZero() || yur.IsAncestor(r.Store, b.Head, other) {
		if b.Work.IsZero() {
			b.Head = other
			if err := r.checkout(work, r.headTree(b)); err != nil {
				return err
			}
			fmt.Printf("Moved forward to %s %s\n", paint(dim, other.Short()), name)
			return nil
		}
	}
	base, err := yur.MergeBase(r.Store, b.Head, other)
	if err != nil {
		return err
	}
	baseTree := yur.EmptyTree(r.Store)
	if !base.IsZero() {
		baseTree = r.revTree(base)
	}
	theirs := r.revTree(other)
	merged, conflicts, err := yur.MergeTrees(r.Store, baseTree, work, theirs, r.State.Branch, name)
	if err != nil {
		return err
	}
	if err := r.checkout(work, merged); err != nil {
		return err
	}
	clean := b.Work.IsZero()
	if err := r.setWork(b, merged); err != nil {
		return err
	}
	parent := other
	b.Merge = &Merge{Name: name, Base: baseTree, Ours: work, Theirs: theirs, Parent: &parent}
	b.Conflicts = conflicts
	if len(conflicts) == 0 && clean {
		rev := yur.Rev{Tree: merged, Parents: []yur.Hash{b.Head, other}, Change: r.workChange(b), Author: me(), Time: time.Now().Unix(), Message: saveMessage}
		if rev.Change == "" {
			rev.Change = yur.NewChange()
		}
		h, err := yur.WriteRev(r.Store, rev)
		if err != nil {
			return err
		}
		b.Head, b.Work, b.Merge = h, yur.Zero, nil
		fmt.Printf("Merged %s %s\n", paint(cyan, name), paint(dim, h.Short()))
		return nil
	}
	if len(conflicts) > 0 {
		fmt.Printf("Merged %s with %s:\n", paint(cyan, name), paint(red, plural(len(conflicts), "conflict")))
		for _, p := range conflicts {
			fmt.Println("  " + paint(red, "!") + " " + p)
		}
		fmt.Println(paint(dim, "Settle them (edit, or yur resolve --ours/--theirs), then yur save. yur merge --abort drops the merge."))
		return nil
	}
	fmt.Printf("Merged %s into your unsaved work: yur save keeps it\n", paint(cyan, name))
	return nil
}

func cmdMerge(r *Repo, a *args) error {
	b := r.State.current()
	if a.bool("abort") {
		if b.Merge == nil {
			return errors.New("no merge to drop")
		}
		work := r.workTree(b)
		if err := r.checkout(work, b.Merge.Ours); err != nil {
			return err
		}
		if err := r.setWork(b, b.Merge.Ours); err != nil {
			return err
		}
		b.Merge, b.Conflicts = nil, nil
		fmt.Println("Merge dropped")
		return nil
	}
	name := a.arg(0)
	if name == "" {
		return errors.New("which branch? yur merge <branch>")
	}
	if b.Merge != nil && len(r.openConflicts(b)) > 0 {
		return errors.New("a merge with conflicts is waiting: settle it and yur save, or yur merge --abort")
	}
	other, err := r.resolve(name)
	if err != nil {
		return err
	}
	return r.mergeInto(b, name, other, "Merge "+name+" into "+r.State.Branch)
}

func cmdResolve(r *Repo, a *args) error {
	b := r.State.current()
	if len(a.list) == 0 {
		if len(b.Conflicts) == 0 {
			fmt.Println(paint(dim, "No conflicts"))
			return nil
		}
		left := map[string]bool{}
		for _, p := range r.openConflicts(b) {
			left[p] = true
		}
		for _, p := range b.Conflicts {
			if left[p] {
				fmt.Println("  " + paint(red, "!") + " " + p)
			} else {
				fmt.Println("  " + paint(green, "✓") + " " + p + paint(dim, " (settled)"))
			}
		}
		return nil
	}
	ours, theirs := a.bool("ours"), a.bool("theirs")
	if ours == theirs {
		return errors.New("pick a side: --ours or --theirs")
	}
	if b.Merge == nil {
		return errors.New("the sides of the conflict are not known; edit the file instead")
	}
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	side := b.Merge.Ours
	if theirs {
		side = b.Merge.Theirs
	}
	work := r.workTree(b)
	edits := map[string]*yur.Entry{}
	for _, p := range paths {
		e, err := yur.Lookup(r.Store, side, p)
		if err != nil {
			edits[p] = nil
			continue
		}
		edits[p] = &e
	}
	target, err := yur.Edit(r.Store, work, edits)
	if err != nil {
		return err
	}
	if err := r.checkout(work, target); err != nil {
		return err
	}
	if err := r.setWork(b, target); err != nil {
		return err
	}
	rest := []string{}
	for _, p := range b.Conflicts {
		if _, done := edits[p]; !done {
			rest = append(rest, p)
		}
	}
	b.Conflicts = rest
	fmt.Printf("Settled %s; yur save when all are\n", plural(len(paths), "file"))
	return nil
}
