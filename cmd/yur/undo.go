package main

import (
	"errors"
	"fmt"
	"strconv"

	"yurba.one/yur"
)

// Undo

func stateWork(r *Repo, s *State) yur.Hash {
	b := s.Branches[s.Branch]
	if b == nil {
		return yur.EmptyTree(r.Store)
	}
	return r.workTree(b)
}

// Goes back to a state, and keeps what was typed since the state being left was made
func (r *Repo) restoreState(target *State, left *State) error {
	current := r.workTree(r.State.current())
	next := target.clone()
	if next.Branches == nil {
		next.Branches = map[string]*Branch{}
	}
	// What was typed on other branches since the state being left stays with them: undoing a switch must
	// not take back the work done after it
	if left != nil {
		for name, nb := range next.Branches {
			now, was := r.State.Branches[name], left.Branches[name]
			if name == r.State.Branch && name == next.Branch || now == nil || was == nil {
				continue
			}
			if now.Work != was.Work {
				nb.Work = now.Work
			}
		}
		// A branch the undo would take away, with work typed on it since: the branch stays, the work in it
		for name, now := range r.State.Branches {
			was, existed := left.Branches[name]
			if _, kept := next.Branches[name]; kept || !existed || now.Work == was.Work {
				continue
			}
			copy := *now
			next.Branches[name] = &copy
			fmt.Println(paint(yellow, "The branch "+name+" stays: it holds work typed after that command."))
		}
	}
	result := stateWork(r, next)
	if left != nil && left.Branch == r.State.Branch && next.Branch == r.State.Branch {
		typedFrom := stateWork(r, left)
		if typedFrom != current {
			merged, conflicts, err := yur.MergeTrees(r.Store, typedFrom, result, current, "restored", "typed since")
			if err != nil {
				return err
			}
			result = merged
			nb := next.current()
			nb.Conflicts = append(nb.Conflicts, conflicts...)
		}
	}
	if err := r.checkout(current, result); err != nil {
		return err
	}
	r.State = next
	return r.setWork(r.State.current(), result)
}

func undoable(list []Op) (map[int]bool, map[int]bool) {
	undone, redone := map[int]bool{}, map[int]bool{}
	byID := map[int]Op{}
	for _, op := range list {
		byID[op.ID] = op
		switch op.Command {
		case "undo":
			undone[op.Target] = true
		case "redo":
			redone[op.Target] = true
			delete(undone, byID[op.Target].Target)
		}
	}
	return undone, redone
}

func isMeta(op Op) bool {
	return op.Command == "snapshot" || op.Command == "undo" || op.Command == "redo"
}

func cmdUndo(r *Repo, a *args) error {
	list := r.ops()
	undone, _ := undoable(list)
	for i := len(list) - 1; i >= 0; i-- {
		op := list[i]
		if isMeta(op) || undone[op.ID] {
			continue
		}
		if err := r.restoreState(op.Before, op.After); err != nil {
			return err
		}
		fmt.Printf("Undid %s %s\n", paint(cyan, "yur "+op.Command), paint(dim, "("+ago(op.Time)+")"))
		return r.record("undo", op.ID)
	}
	return errors.New("nothing to undo")
}

func cmdRedo(r *Repo, a *args) error {
	list := r.ops()
	_, redone := undoable(list)
	for i := len(list) - 1; i >= 0; i-- {
		op := list[i]
		if op.Command == "snapshot" || op.Command == "redo" {
			continue
		}
		if op.Command != "undo" || redone[op.ID] {
			break
		}
		if err := r.restoreState(op.Before, op.After); err != nil {
			return err
		}
		fmt.Println("Redone")
		return r.record("redo", op.ID)
	}
	return errors.New("nothing to redo: redo follows an undo")
}

func cmdOps(r *Repo, a *args) error {
	list := r.ops()
	undone, _ := undoable(list)
	limit, _ := strconv.Atoi(a.get("n"))
	if limit <= 0 {
		limit = 20
	}
	shown := 0
	for i := len(list) - 1; i >= 0 && shown < limit; i-- {
		op := list[i]
		if op.Command == "snapshot" {
			continue
		}
		mark := ""
		if undone[op.ID] {
			mark = paint(dim, " (undone)")
		}
		fmt.Printf("%s  %-14s yur %s%s\n", paint(dim, fmt.Sprintf("#%d", op.ID)), ago(op.Time), op.Command, mark)
		shown++
	}
	return nil
}
