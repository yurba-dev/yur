package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"yurba.one/yur"
)

// The way back to git: every saved rev becomes a commit, every branch a branch and every tag a tag, through
// `git fast-import`. It goes on from where the last export stopped, so a mirror (on GitHub, say) is kept
// with yur export-git and git push.
//
// The marks tie the two together: the export's own file gives each yur object a number, and git keeps
// the commit each number became.
type gitExport struct {
	Folder string         `json:"folder"`
	Next   int            `json:"next"`
	Marks  map[string]int `json:"marks"`
	// The refs the last export wrote: those gone from yur since are taken out of git too
	Refs []string `json:"refs"`
}

const exportRef = "refs/yur/export"

func cmdExportGit(r *Repo, a *args) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is needed to write a git repository: install it first")
	}
	folder := a.arg(0)
	if folder == "" {
		// A folder with git's own history (from import-git) would have its branches replaced by these
		if _, err := os.Stat(filepath.Join(r.Root, ".git")); err == nil {
			return errors.New("this folder has a git history of its own: give the export a folder of its own, e.g. yur export-git ../" + filepath.Base(r.Root) + "-git")
		}
		folder = r.Root
	}
	root, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
			return errors.New("git init: " + strings.TrimSpace(string(out)))
		}
	}

	stateFile := filepath.Join(r.Dir, "git-export.json")
	marksFile := filepath.Join(r.Dir, "git-marks")
	exp := &gitExport{Marks: map[string]int{}}
	if data, err := os.ReadFile(stateFile); err == nil {
		json.Unmarshal(data, exp)
	}
	// Numbers mean something only to the repository they were made for
	if exp.Folder != root || exp.Marks == nil {
		exp = &gitExport{Folder: root, Marks: map[string]int{}}
		os.Remove(marksFile)
	}

	heads := map[string]yur.Hash{}
	for name, b := range r.State.Branches {
		if !b.Head.IsZero() {
			heads["refs/heads/"+name] = b.Head
		}
	}
	for name, h := range r.State.Tags {
		heads["refs/tags/"+name] = h
	}
	if len(heads) == 0 {
		return errors.New("nothing saved to export yet")
	}
	start := []yur.Hash{}
	for _, h := range heads {
		start = append(start, h)
	}
	order, revs, err := yur.Ancestors(r.Store, start, nil, 0)
	if err != nil {
		return err
	}

	// --force: the branches there are this export's own, and an amend or a sync here moves them back too
	cmd := exec.Command("git", "-C", root, "fast-import", "--quiet", "--force", "--import-marks-if-exists="+marksFile, "--export-marks="+marksFile)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	w := bufio.NewWriterSize(in, 1<<20)
	fmt.Fprintln(w, "feature done")
	written := 0
	// Parents first, whatever the times say
	for _, h := range yur.Topo(order, revs) {
		if _, done := exp.Marks[h.String()]; done {
			continue
		}
		if err := exp.commit(w, r, h, revs[h]); err != nil {
			in.Close()
			cmd.Wait()
			return err
		}
		written++
	}
	refs := []string{}
	for ref := range heads {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		fmt.Fprintf(w, "reset %s\nfrom :%d\n\n", ref, exp.Marks[heads[ref].String()])
	}
	fmt.Fprintln(w, "done")
	if err := w.Flush(); err != nil {
		return err
	}
	in.Close()
	failed := cmd.Wait()
	exec.Command("git", "-C", root, "update-ref", "-d", exportRef).Run()
	if failed != nil {
		return errors.New("git fast-import stopped: " + failed.Error())
	}
	for _, ref := range exp.Refs {
		if _, still := heads[ref]; !still {
			exec.Command("git", "-C", root, "update-ref", "-d", ref).Run()
		}
	}
	exp.Refs = refs
	data, _ := json.Marshal(exp)
	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		return err
	}
	fmt.Printf("Exported %s into %s: %s\n", plural(written, "new rev"), root, strings.Join(refs, ", "))
	fmt.Println(paint(dim, "git push --all and git push --tags send them on, e.g. to a mirror on GitHub."))
	return nil
}

func (x *gitExport) mark(h yur.Hash) int {
	x.Next++
	x.Marks[h.String()] = x.Next
	return x.Next
}

func (x *gitExport) commit(w *bufio.Writer, r *Repo, h yur.Hash, rev yur.Rev) error {
	before := yur.EmptyTree(r.Store)
	if len(rev.Parents) > 0 {
		before = r.revTree(rev.Parents[0])
	}
	changes, err := yur.DiffTrees(r.Store, before, rev.Tree)
	if err != nil {
		return err
	}
	// The files first, each once for the whole history
	for _, c := range changes {
		if c.New == nil {
			continue
		}
		if _, done := x.Marks[c.New.Hash.String()]; done {
			continue
		}
		data, err := yur.ReadFile(r.Store, c.New.Hash)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n", x.mark(c.New.Hash), len(data))
		w.Write(data)
		w.WriteString("\n")
	}
	who := gitName(rev.Author.Name) + " <> " + strconv.FormatInt(rev.Time, 10) + " +0000"
	// A rev without parents starts from nothing, not from where the last one was put
	fmt.Fprintf(w, "reset %s\ncommit %s\nmark :%d\nauthor %s\ncommitter %s\ndata %d\n%s\n", exportRef, exportRef, x.mark(h), who, who, len(rev.Message), rev.Message)
	for i, p := range rev.Parents {
		m, ok := x.Marks[p.String()]
		if !ok {
			return errors.New("the parent of " + h.Short() + " is missing from the history")
		}
		if i == 0 {
			fmt.Fprintf(w, "from :%d\n", m)
		} else {
			fmt.Fprintf(w, "merge :%d\n", m)
		}
	}
	for _, c := range changes {
		if c.New == nil {
			fmt.Fprintf(w, "D %s\n", gitQuote(c.Path))
			continue
		}
		mode := "100644"
		switch c.New.Kind {
		case yur.KindExec:
			mode = "100755"
		case yur.KindLink:
			mode = "120000"
		}
		fmt.Fprintf(w, "M %s :%d %s\n", mode, x.Marks[c.New.Hash.String()], gitQuote(c.Path))
	}
	w.WriteString("\n")
	return nil
}

// Git reads a name up to the address that follows it, so none of its marks may be in it
func gitName(name string) string {
	name = strings.NewReplacer("<", "", ">", "", "\n", " ").Replace(strings.TrimSpace(name))
	if name == "" {
		return "someone"
	}
	return name
}

func gitQuote(p string) string {
	if strings.ContainsAny(p, "\"\n\\") || strings.HasPrefix(p, " ") {
		return strconv.Quote(p)
	}
	return p
}
