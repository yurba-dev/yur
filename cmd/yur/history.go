package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"yurba.one/yur"
)

// Revert and pick: the changes of one rev, taken back or brought over onto where you are

func cmdRevert(r *Repo, a *args) error {
	h, rev, err := r.oneRev(a, "revert")
	if err != nil {
		return err
	}
	b := r.State.current()
	work := r.workTree(b)
	before := r.parentTree(h)
	// The rev's own change undone: from its tree back to its parent's, laid over the work
	merged, conflicts, err := yur.MergeTrees(r.Store, rev.Tree, work, before, r.State.Branch, "revert")
	if err != nil {
		return err
	}
	message := "Revert \"" + rev.Title() + "\"\n\nThis takes back " + h.String() + "."
	return r.applyRev(b, a, work, merged, before, conflicts, message, nil, "Reverted "+paint(dim, h.Short())+" "+rev.Title())
}

func cmdPick(r *Repo, a *args) error {
	h, rev, err := r.oneRev(a, "pick")
	if err != nil {
		return err
	}
	b := r.State.current()
	if yur.IsAncestor(r.Store, h, b.Head) {
		return errors.New("this rev is in the branch already")
	}
	work := r.workTree(b)
	merged, conflicts, err := yur.MergeTrees(r.Store, r.parentTree(h), work, rev.Tree, r.State.Branch, "picked")
	if err != nil {
		return err
	}
	author := rev.Author
	return r.applyRev(b, a, work, merged, rev.Tree, conflicts, rev.Message, &author, "Picked "+paint(dim, h.Short())+" "+rev.Title())
}

func (r *Repo) oneRev(a *args, command string) (yur.Hash, yur.Rev, error) {
	name := a.arg(0)
	if name == "" {
		return yur.Zero, yur.Rev{}, errors.New("which rev? yur " + command + " <rev>")
	}
	b := r.State.current()
	if b.Merge != nil && len(r.openConflicts(b)) > 0 {
		return yur.Zero, yur.Rev{}, errors.New("conflicts are still open: settle them and yur save first")
	}
	h, err := r.resolve(name)
	if err != nil {
		return yur.Zero, yur.Rev{}, err
	}
	rev, err := yur.ReadRev(r.Store, h)
	if err != nil {
		return yur.Zero, yur.Rev{}, err
	}
	if len(rev.Parents) > 1 {
		fmt.Println(paint(dim, "A merge: its changes are taken against its first parent, the branch it was merged into."))
	}
	return h, rev, nil
}

// The result laid on the files. With nothing else unsaved and no conflicts it is saved as a rev right
// away; otherwise it waits in the work, and yur save keeps it with the message prepared here.
func (r *Repo) applyRev(b *Branch, a *args, work, result, theirs yur.Hash, conflicts []string, message string, author *yur.Author, done string) error {
	if result == work {
		fmt.Println(paint(dim, "Nothing changes: the files are that way already, or have moved on too far from that rev"))
		return nil
	}
	clean := b.Work.IsZero()
	if err := r.checkout(work, result); err != nil {
		return err
	}
	if err := r.setWork(b, result); err != nil {
		return err
	}
	if len(conflicts) > 0 {
		// The rev's message and author wait for the save that settles it
		b.Merge = &Merge{Name: "the rev", Base: work, Ours: work, Theirs: theirs, Message: message, Author: author}
		b.Conflicts = append(b.Conflicts, conflicts...)
		fmt.Println(done + " with " + paint(red, plural(len(conflicts), "conflict")) + ":")
		for _, p := range conflicts {
			fmt.Println("  " + paint(red, "!") + " " + p)
		}
		fmt.Println(paint(dim, "Settle them (edit, or yur resolve --ours/--theirs), then yur save; yur undo takes it all back."))
		return nil
	}
	// Mixed into other unsaved work: the save is theirs, with a message of its own
	if !clean {
		fmt.Println(done + paint(dim, " into your unsaved work: yur save keeps it with the rest"))
		return nil
	}
	if err := r.checkSecrets(r.headTree(b), result, a); err != nil {
		b.Merge = &Merge{Name: "the rev", Base: work, Ours: work, Theirs: theirs, Message: message, Author: author}
		return err
	}
	rev := yur.Rev{Tree: result, Change: r.workChange(b), Author: me(), Time: time.Now().Unix(), Message: message}
	if author != nil {
		rev.Author = *author
	}
	if !b.Head.IsZero() {
		rev.Parents = []yur.Hash{b.Head}
	}
	h, err := yur.WriteRev(r.Store, rev)
	if err != nil {
		return err
	}
	b.Head, b.Work = h, yur.Zero
	fmt.Printf("%s %s\n", done, paint(dim, "→ "+h.Short()))
	return nil
}

// Blame: who brought in each line

func cmdBlame(r *Repo, a *args) error {
	paths, err := r.paths(a.list)
	if err != nil {
		return err
	}
	if len(paths) != 1 {
		return errors.New("which file? yur blame <path> [-r rev]")
	}
	h, err := r.resolve(a.get("r", "rev"))
	if err != nil {
		return err
	}
	if h.IsZero() {
		return errors.New("no revs yet")
	}
	lines, owners, revs, err := yur.Blame(r.Store, h, paths[0], 5000)
	if err != nil {
		if err.Error() == "path_not_found" {
			return errors.New(paths[0] + " is not in that rev")
		}
		if err.Error() == "not_text" {
			return errors.New(paths[0] + " is not text")
		}
		return err
	}
	b := r.State.current()
	labels := make([]string, len(revs))
	for i, rh := range revs {
		rev, err := yur.ReadRev(r.Store, rh)
		if err != nil {
			continue
		}
		name := rev.Author.Name
		if name == "" {
			name = "someone"
		}
		if len([]rune(name)) > 16 {
			name = string([]rune(name)[:16])
		}
		change := rev.Change
		if len(change) > 8 {
			change = change[:8]
		}
		if rh == b.Work {
			labels[i] = paint(green, fmt.Sprintf("%-8s", "unsaved")) + " " + fmt.Sprintf("%-16s", name) + " " + fmt.Sprintf("%-11s", "now")
		} else {
			labels[i] = paint(purple, fmt.Sprintf("%-8s", change)) + " " + fmt.Sprintf("%-16s", name) + " " + paint(dim, fmt.Sprintf("%-11s", time.Unix(rev.Time, 0).Format("2006-01-02")))
		}
	}
	width := len(strconv.Itoa(len(lines)))
	for i, line := range lines {
		label := paint(dim, fmt.Sprintf("%-8s %-16s %-11s", "?", "", ""))
		if owners[i] >= 0 {
			label = labels[owners[i]]
		}
		fmt.Printf("%s %s %s\n", label, paint(dim, fmt.Sprintf("%*d│", width, i+1)), strings.TrimRight(line, "\r\n"))
	}
	return nil
}

// Grep: lines that match, in every file of a rev or of the work

func cmdGrep(r *Repo, a *args) error {
	pattern := a.arg(0)
	if pattern == "" {
		return errors.New("what to look for? yur grep <pattern> [paths] [-i] [-l] [-r rev]")
	}
	if a.bool("i") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errors.New("the pattern is not a regular expression: " + err.Error())
	}
	paths, err := r.paths(a.list[1:])
	if err != nil {
		return err
	}
	tree := r.workTree(r.State.current())
	if rev := a.get("r", "rev"); rev != "" {
		h, err := r.resolve(rev)
		if err != nil {
			return err
		}
		tree = r.revTree(h)
	}
	files, err := yur.Flatten(r.Store, tree)
	if err != nil {
		return err
	}
	names := []string{}
	for p, e := range files {
		if e.Kind == yur.KindLink || e.Size > 4<<20 {
			continue
		}
		if len(paths) > 0 && len(filterPaths([]yur.FileChange{{Path: p}}, paths)) == 0 {
			continue
		}
		names = append(names, p)
	}
	sort.Strings(names)
	found := 0
	for _, p := range names {
		data, err := yur.ReadFile(r.Store, files[p].Hash)
		if err != nil || yur.IsBinary(data) {
			continue
		}
		for n, line := range yur.SplitLines(data) {
			line = strings.TrimRight(line, "\r\n")
			hits := re.FindAllStringIndex(line, -1)
			if len(hits) == 0 {
				continue
			}
			found++
			if a.bool("l") {
				fmt.Println(paint(purple, p))
				break
			}
			var shown strings.Builder
			last := 0
			for _, m := range hits {
				shown.WriteString(line[last:m[0]])
				shown.WriteString(paint(red, line[m[0]:m[1]]))
				last = m[1]
			}
			shown.WriteString(line[last:])
			fmt.Printf("%s%s%s%s %s\n", paint(purple, p), paint(dim, ":"), paint(green, strconv.Itoa(n+1)), paint(dim, ":"), shown.String())
		}
	}
	if found == 0 {
		return errors.New("nothing found")
	}
	return nil
}
