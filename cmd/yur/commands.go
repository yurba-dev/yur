package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yurba.one/yur"
)

func cmdInit(_ *Repo, a *args) error {
	folder := a.arg(0)
	if folder == "" {
		folder = "."
	}
	root, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	branch := a.get("branch", "b")
	if branch == "" {
		branch = "main"
	}
	if !yur.ValidRef(branch) {
		return errors.New("a branch name is letters, digits and - _ . / only")
	}
	r, err := initRepo(root, branch)
	if err != nil {
		return err
	}
	if err := r.snapshot(); err != nil {
		return err
	}
	r.saveState()
	fmt.Printf("Started a yur project in %s, on branch %s\n", root, paint(cyan, branch))
	if !r.State.current().Work.IsZero() {
		fmt.Println(paint(dim, "yur save -m \"First version\" saves what is here."))
	}
	return nil
}

// Save

func ask(question string) string {
	if !isTerminal {
		return ""
	}
	fmt.Print(question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// The tree with only the changes under paths taken from work; all of them without paths
func (r *Repo) partTree(base, work yur.Hash, paths []string) (yur.Hash, error) {
	if len(paths) == 0 {
		return work, nil
	}
	list, err := yur.DiffTrees(r.Store, base, work)
	if err != nil {
		return yur.Zero, err
	}
	list = filterPaths(list, paths)
	if len(list) == 0 {
		return yur.Zero, errors.New("nothing changed under " + strings.Join(paths, ", "))
	}
	edits := map[string]*yur.Entry{}
	for _, c := range list {
		edits[c.Path] = c.New
	}
	return yur.Edit(r.Store, base, edits)
}

func (r *Repo) checkSecrets(from, to yur.Hash, a *args) error {
	if a.bool("allow-secrets") {
		return nil
	}
	list, err := yur.DiffTrees(r.Store, from, to)
	if err != nil {
		return err
	}
	found := []yur.Secret{}
	for _, c := range list {
		if c.New != nil && c.New.Kind != yur.KindLink {
			found = append(found, yur.FindSecrets(c.Path, r.fileData(c.New))...)
		}
	}
	if len(found) > 0 {
		printSecrets(found)
		return errors.New("nothing saved")
	}
	return nil
}

func cmdSave(r *Repo, a *args) error {
	b := r.State.current()
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	if b.Work.IsZero() && (b.Merge == nil || b.Merge.Parent == nil) {
		return errors.New("nothing to save: no changes")
	}
	base := r.headTree(b)
	tree, err := r.partTree(base, r.workTree(b), paths)
	if err != nil {
		return err
	}
	if left := r.openConflicts(b); len(left) > 0 && !a.bool("with-conflicts") {
		for _, p := range left {
			fmt.Println("  " + paint(red, "!") + " " + p)
		}
		return errors.New("these files still have conflict markers (settle them, or --with-conflicts saves them as they are)")
	}
	if err := r.checkSecrets(base, tree, a); err != nil {
		return err
	}
	message := a.get("m", "message")
	if message == "" && b.Merge != nil && b.Merge.Message != "" {
		message = b.Merge.Message
	}
	if message == "" {
		message = ask("What changed? ")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("a rev needs a message: yur save -m \"what changed\"")
	}
	change := r.workChange(b)
	if change == "" || len(paths) > 0 {
		change = yur.NewChange()
	}
	rev := yur.Rev{Tree: tree, Change: change, Author: me(), Time: time.Now().Unix(), Message: message}
	// A picked rev keeps who made it
	if b.Merge != nil && b.Merge.Author != nil {
		rev.Author = *b.Merge.Author
	}
	if !b.Head.IsZero() {
		rev.Parents = []yur.Hash{b.Head}
	}
	if b.Merge != nil && b.Merge.Parent != nil {
		rev.Parents = append(rev.Parents, *b.Merge.Parent)
	}
	if a.bool("with-conflicts") {
		rev.Conflicts = r.openConflicts(b)
	}
	h, err := yur.WriteRev(r.Store, rev)
	if err != nil {
		return err
	}
	work := r.workTree(b)
	b.Head = h
	b.Work = yur.Zero
	b.Merge = nil
	b.Conflicts = nil
	if err := r.setWork(b, work); err != nil {
		return err
	}
	list, _ := r.changes(base, tree, nil)
	added, removed := 0, 0
	for _, st := range list {
		added += st.added
		removed += st.removed
	}
	fmt.Printf("Saved %s %s %s\n", paint(purple, change[:8]), paint(dim, h.Short()), rev.Title())
	fmt.Printf("%s, %s %s\n", plural(len(list), "file"), paint(green, "+"+strconv.Itoa(added)), paint(red, "-"+strconv.Itoa(removed)))
	if !b.Work.IsZero() {
		fmt.Println(paint(dim, "Other changes stay unsaved."))
	}
	return nil
}

func cmdAmend(r *Repo, a *args) error {
	b := r.State.current()
	if b.Head.IsZero() {
		return errors.New("there is no rev to amend yet")
	}
	head, err := yur.ReadRev(r.Store, b.Head)
	if err != nil {
		return err
	}
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	tree := head.Tree
	if !b.Work.IsZero() {
		if tree, err = r.partTree(head.Tree, r.workTree(b), paths); err != nil {
			return err
		}
	}
	if err := r.checkSecrets(head.Tree, tree, a); err != nil {
		return err
	}
	message := a.get("m", "message")
	if message == "" {
		message = head.Message
	}
	if tree == head.Tree && message == head.Message {
		return errors.New("nothing to amend: no changes and the same message")
	}
	if r.State.Remote != nil && r.State.Remote.Branches[r.State.Branch] == b.Head {
		fmt.Println(paint(yellow, "This rev is on the server already: the next push needs --force."))
	}
	head.Tree, head.Message, head.Time = tree, message, time.Now().Unix()
	h, err := yur.WriteRev(r.Store, head)
	if err != nil {
		return err
	}
	work := r.workTree(b)
	b.Head, b.Work = h, yur.Zero
	if err := r.setWork(b, work); err != nil {
		return err
	}
	fmt.Printf("Amended %s %s %s\n", paint(purple, head.Change[:8]), paint(dim, h.Short()), head.Title())
	return nil
}

func cmdRestore(r *Repo, a *args) error {
	b := r.State.current()
	source := r.headTree(b)
	if from := a.get("from"); from != "" {
		h, err := r.resolve(from)
		if err != nil {
			return err
		}
		source = r.revTree(h)
	}
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	current := r.workTree(b)
	target := source
	if len(paths) > 0 {
		list, err := yur.DiffTrees(r.Store, current, source)
		if err != nil {
			return err
		}
		edits := map[string]*yur.Entry{}
		for _, c := range filterPaths(list, paths) {
			edits[c.Path] = c.New
		}
		if len(edits) == 0 {
			fmt.Println(paint(dim, "Those files are as they were already"))
			return nil
		}
		if target, err = yur.Edit(r.Store, current, edits); err != nil {
			return err
		}
	}
	list, _ := yur.DiffTrees(r.Store, current, target)
	if err := r.checkout(current, target); err != nil {
		return err
	}
	if err := r.setWork(b, target); err != nil {
		return err
	}
	fmt.Printf("Restored %s %s\n", plural(len(list), "file"), paint(dim, "(yur undo brings the changes back)"))
	return nil
}

func cmdIgnore(r *Repo, a *args) error {
	if len(a.list) == 0 {
		return errors.New("what to ignore? yur ignore <pattern>, e.g. yur ignore dist/")
	}
	file := filepath.Join(r.Root, ".yurignore")
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, p := range a.list {
		fmt.Fprintln(f, p)
	}
	fmt.Println("Added to .yurignore. Files it matches leave the project, but stay in the folder.")
	return nil
}
