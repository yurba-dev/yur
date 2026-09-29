package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yurba.one/yur"
)

// Reads `git fast-export`: every commit becomes a rev with its author, time and message, every branch a
// branch. The folder then is a yur project too; its files stay where they are.
func cmdImportGit(_ *Repo, a *args) error {
	folder := a.arg(0)
	if folder == "" {
		folder = "."
	}
	root, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is needed to read the history: install it, import, and you can remove it after")
	}
	current, _ := exec.Command("git", "-C", root, "symbolic-ref", "--short", "HEAD").Output()
	branch := strings.TrimSpace(string(current))
	if branch == "" {
		branch = "main"
	}
	r, err := initRepo(root, branch)
	if err != nil {
		return err
	}
	if err := r.acquire(); err != nil {
		return err
	}
	defer r.release()

	cmd := exec.Command("git", "-C", root, "fast-export", "--all", "--reencode=yes", "--signed-tags=strip", "--tag-of-filtered-object=drop", "--use-done-feature")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Println("Reading the git history...")
	imp := &gitImport{r: r, blobs: map[string]yur.Entry{}, commits: map[string]yur.Hash{}, trees: map[yur.Hash]yur.Hash{}, branches: map[string]yur.Hash{}, tags: map[string]yur.Hash{}}
	err = imp.read(bufio.NewReaderSize(out, 1<<20))
	if err == nil && !imp.done {
		err = errors.New("git fast-export stopped before the end of the history")
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		os.RemoveAll(r.Dir)
		return err
	}
	// A history cut short by git itself is no import
	if err := cmd.Wait(); err != nil {
		os.RemoveAll(r.Dir)
		return errors.New("git fast-export failed: " + err.Error())
	}

	r.State.Branches = map[string]*Branch{}
	for name, h := range imp.branches {
		r.State.Branches[name] = &Branch{Head: h}
	}
	if len(imp.tags) > 0 {
		r.State.Tags = imp.tags
	}
	if _, ok := r.State.Branches[branch]; !ok {
		r.State.Branches[branch] = &Branch{}
	}
	r.State.Branch = branch
	// The folder as it is: changes git had not committed become unsaved work
	if err := r.snapshot(); err != nil {
		return err
	}
	r.before = r.State.clone()
	if err := r.saveState(); err != nil {
		return err
	}
	fmt.Printf("Imported %s on %s and %s. The .git folder is left alone; yur create puts the project on Yurba.\n",
		plural(imp.count, "rev"), plural(len(imp.branches), "branch"), plural(len(imp.tags), "tag"))
	return nil
}

type gitImport struct {
	r        *Repo
	blobs    map[string]yur.Entry
	commits  map[string]yur.Hash
	trees    map[yur.Hash]yur.Hash
	branches map[string]yur.Hash
	tags     map[string]yur.Hash
	count    int
	// The stream ended with its done, not cut off
	done bool
}

func readData(br *bufio.Reader, line string) ([]byte, error) {
	size, err := strconv.Atoi(strings.TrimPrefix(line, "data "))
	if err != nil {
		return nil, errors.New("unexpected git output: " + line)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(br, data); err != nil {
		return nil, err
	}
	return data, nil
}

func gitPath(s string) string {
	if strings.HasPrefix(s, "\"") {
		if unq, err := strconv.Unquote(s); err == nil {
			return unq
		}
	}
	return s
}

func (g *gitImport) read(br *bufio.Reader) error {
	var pending string
	next := func() (string, error) {
		if pending != "" {
			line := pending
			pending = ""
			return line, nil
		}
		line, err := br.ReadString('\n')
		if err == io.EOF && line != "" {
			return line, nil
		}
		return strings.TrimSuffix(line, "\n"), err
	}
	for {
		line, err := next()
		if line == "done" {
			g.done = true
			return nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, "tag "):
			if err := g.tag(strings.TrimPrefix(line, "tag "), next, br); err != nil {
				return err
			}
		case line == "blob":
			mark := ""
			for {
				l, err := next()
				if err != nil {
					return err
				}
				if strings.HasPrefix(l, "mark ") {
					mark = strings.TrimPrefix(l, "mark ")
				} else if strings.HasPrefix(l, "data ") {
					data, err := readData(br, l)
					if err != nil {
						return err
					}
					h, err := yur.WriteFile(g.r.Store, data)
					if err != nil {
						return err
					}
					g.blobs[mark] = yur.Entry{Hash: h, Size: int64(len(data))}
					break
				}
			}
		case strings.HasPrefix(line, "commit "):
			if err := g.commit(strings.TrimPrefix(line, "commit "), next, br, func(l string) { pending = l }); err != nil {
				return err
			}
		case strings.HasPrefix(line, "reset "):
			ref := strings.TrimPrefix(line, "reset ")
			l, err := next()
			if err != nil {
				return err
			}
			if strings.HasPrefix(l, "from ") {
				if h, ok := g.commits[strings.TrimPrefix(l, "from ")]; ok {
					g.setRef(ref, h)
				}
			} else {
				pending = l
			}
		}
	}
}

func (g *gitImport) setRef(ref string, h yur.Hash) {
	if name := strings.TrimPrefix(ref, "refs/heads/"); name != ref && yur.ValidRef(name) {
		g.branches[name] = h
	}
	if name := strings.TrimPrefix(ref, "refs/tags/"); name != ref && yur.ValidRef(name) {
		g.tags[name] = h
	}
}

// An annotated tag: its name on the commit it points at. Its message is read past, so no line of it can
// pass for a command of the stream.
func (g *gitImport) tag(name string, next func() (string, error), br *bufio.Reader) error {
	for {
		l, err := next()
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(l, "from "):
			if h, ok := g.commits[strings.TrimPrefix(l, "from ")]; ok && yur.ValidRef(name) {
				g.tags[name] = h
			}
		case strings.HasPrefix(l, "data "):
			_, err := readData(br, l)
			return err
		}
	}
}

func (g *gitImport) commit(ref string, next func() (string, error), br *bufio.Reader, unread func(string)) error {
	var mark, message string
	var author yur.Author
	var when int64
	parents := []yur.Hash{}
	edits := map[string]*yur.Entry{}
	clear := false
	for {
		l, err := next()
		if err != nil && l == "" {
			return err
		}
		switch {
		case strings.HasPrefix(l, "mark "):
			mark = strings.TrimPrefix(l, "mark ")
		case strings.HasPrefix(l, "author "):
			author, when = gitPerson(strings.TrimPrefix(l, "author "))
		case strings.HasPrefix(l, "committer "):
			if when == 0 {
				_, when = gitPerson(strings.TrimPrefix(l, "committer "))
			}
		case strings.HasPrefix(l, "data "):
			data, err := readData(br, l)
			if err != nil {
				return err
			}
			message = string(data)
		case strings.HasPrefix(l, "from ") || strings.HasPrefix(l, "merge "):
			_, ref, _ := strings.Cut(l, " ")
			if h, ok := g.commits[ref]; ok {
				parents = append(parents, h)
			}
		case l == "deleteall":
			clear = true
		case strings.HasPrefix(l, "M "):
			parts := strings.SplitN(l, " ", 4)
			if len(parts) < 4 {
				continue
			}
			p, ok := yur.CleanPath(gitPath(parts[3]))
			blob, found := g.blobs[parts[2]]
			if !ok || !found || parts[1] == "160000" {
				continue
			}
			kind := yur.KindFile
			switch parts[1] {
			case "100755":
				kind = yur.KindExec
			case "120000":
				kind = yur.KindLink
			}
			edits[p] = &yur.Entry{Kind: kind, Hash: blob.Hash, Size: blob.Size}
		case strings.HasPrefix(l, "D "):
			if p, ok := yur.CleanPath(gitPath(strings.TrimPrefix(l, "D "))); ok {
				edits[p] = nil
			}
		case strings.HasPrefix(l, "original-oid ") || strings.HasPrefix(l, "encoding "):
		case l == "" && message != "":
			return g.finish(ref, mark, author, when, message, parents, edits, clear)
		default:
			unread(l)
			return g.finish(ref, mark, author, when, message, parents, edits, clear)
		}
	}
}

func (g *gitImport) finish(ref, mark string, author yur.Author, when int64, message string, parents []yur.Hash, edits map[string]*yur.Entry, clear bool) error {
	base := yur.EmptyTree(g.r.Store)
	if len(parents) > 0 && !clear {
		base = g.r.revTree(parents[0])
	}
	tree, err := yur.Edit(g.r.Store, base, edits)
	if err != nil {
		return err
	}
	if when == 0 {
		when = time.Now().Unix()
	}
	rev := yur.Rev{Tree: tree, Parents: parents, Change: yur.NewChange(), Author: author, Time: when, Message: strings.TrimRight(message, "\n")}
	h, err := yur.WriteRev(g.r.Store, rev)
	if err != nil {
		return err
	}
	g.commits[mark] = h
	g.setRef(ref, h)
	g.count++
	if g.count%500 == 0 {
		fmt.Printf("  %d revs\n", g.count)
	}
	return nil
}

// "Name <email> 1600000000 +0300": the name is kept, the address is not (it stays private on Yurba)
func gitPerson(s string) (yur.Author, int64) {
	name := s
	if i := strings.Index(s, " <"); i >= 0 {
		name = s[:i]
	}
	var when int64
	if j := strings.LastIndex(s, "> "); j >= 0 {
		fields := strings.Fields(s[j+2:])
		if len(fields) > 0 {
			when, _ = strconv.ParseInt(fields[0], 10, 64)
		}
	}
	return yur.Author{Name: string(bytes.TrimSpace([]byte(name)))}, when
}
