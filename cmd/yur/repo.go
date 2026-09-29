package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yurba.one/yur"
)

type Branch struct {
	Head yur.Hash `json:"head"`
	// The unsaved changes as a rev on top of Head, kept on every command so nothing typed is ever lost
	Work yur.Hash `json:"work,omitempty"`
	// A merge waiting to be saved: the other side and what it was merged from
	Merge *Merge `json:"merge,omitempty"`
	// Files left with conflict markers, by a merge or a sync
	Conflicts []string `json:"conflicts,omitempty"`
}

type Merge struct {
	Name   string   `json:"name"`
	Base   yur.Hash `json:"base"`
	Ours   yur.Hash `json:"ours"`
	Theirs yur.Hash `json:"theirs"`
	// Nil for the sides of a sync, which saves no merge rev
	Parent *yur.Hash `json:"parent,omitempty"`
	// What yur save says when no -m is given: a revert or a pick left waiting for its conflicts
	Message string      `json:"message,omitempty"`
	Author  *yur.Author `json:"author,omitempty"`
}

type Remote struct {
	Project  string              `json:"project"`
	Branches map[string]yur.Hash `json:"branches"`
	Default  string              `json:"default"`
	Tags     map[string]yur.Hash `json:"tags,omitempty"`
	// Files of old revs are fetched when first needed
	Light bool `json:"light"`
	// Branches the server had and dropped since: sync doesn't bring them back on its own
	Gone []string `json:"gone,omitempty"`
}

type State struct {
	Version  int                 `json:"version"`
	Branch   string              `json:"branch"`
	Branches map[string]*Branch  `json:"branches"`
	Tags     map[string]yur.Hash `json:"tags,omitempty"`
	Remote   *Remote             `json:"remote,omitempty"`
}

func (s *State) clone() *State {
	data, _ := json.Marshal(s)
	var c State
	json.Unmarshal(data, &c)
	return &c
}

func (s *State) current() *Branch {
	b := s.Branches[s.Branch]
	if b == nil {
		b = &Branch{}
		s.Branches[s.Branch] = b
	}
	return b
}

func (s *State) branchNames() []string {
	names := []string{}
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type Repo struct {
	Root  string
	Dir   string
	Store yur.Store
	State *State
	// The state as the command found it, for the operation log
	before *State
	lock   string
	stop   chan struct{}
}

func findRoot(from string) (string, error) {
	dir, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".yur")); err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not a yur project here (yur init starts one, yur clone brings one)")
		}
		dir = parent
	}
}

func openRepo() (*Repo, error) {
	root, err := findRoot(".")
	if err != nil {
		return nil, err
	}
	return loadRepo(root)
}

func loadRepo(root string) (*Repo, error) {
	dir := filepath.Join(root, ".yur")
	r := &Repo{Root: root, Dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, errors.New("the project's state is unreadable: " + err.Error())
	}
	r.State = &State{}
	if err := json.Unmarshal(data, r.State); err != nil {
		return nil, errors.New("the project's state is damaged: " + err.Error())
	}
	if r.State.Branches == nil {
		r.State.Branches = map[string]*Branch{}
	}
	r.before = r.State.clone()
	r.Store = &lazyStore{local: yur.FileStore{Dir: filepath.Join(dir, "objects")}, repo: r}
	return r, nil
}

func initRepo(root string, branch string) (*Repo, error) {
	dir := filepath.Join(root, ".yur")
	if _, err := os.Stat(dir); err == nil {
		return nil, errors.New("there is a yur project here already")
	}
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0755); err != nil {
		return nil, err
	}
	hideDir(dir)
	state := &State{Version: 1, Branch: branch, Branches: map[string]*Branch{branch: {}}}
	r := &Repo{Root: root, Dir: dir, State: state, before: state.clone()}
	r.Store = &lazyStore{local: yur.FileStore{Dir: filepath.Join(dir, "objects")}, repo: r}
	return r, r.saveState()
}

func (r *Repo) saveState() error {
	data, err := json.MarshalIndent(r.State, "", "\t")
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.Dir, "state.json.tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.Dir, "state.json"))
}

// One command at a time: two at once could each save a state the other never saw
func (r *Repo) acquire() error {
	r.lock = filepath.Join(r.Dir, "lock")
	for i := 0; i < 2; i++ {
		f, err := os.OpenFile(r.lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString(time.Now().Format(time.RFC3339))
			f.Close()
			r.keepLock()
			return nil
		}
		// A lock left by a command that died long ago is taken over
		if info, err := os.Stat(r.lock); err == nil && time.Since(info.ModTime()) > 10*time.Minute {
			os.Remove(r.lock)
			continue
		}
		break
	}
	r.lock = ""
	return errors.New("another yur command is running in this project (if none is, delete .yur/lock)")
}

// A long clone or push keeps its lock fresh, or another command would take it for one left by a crash
func (r *Repo) keepLock() {
	r.stop = make(chan struct{})
	lock, stop := r.lock, r.stop
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				now := time.Now()
				os.Chtimes(lock, now, now)
			case <-stop:
				return
			}
		}
	}()
}

func (r *Repo) release() {
	if r.stop != nil {
		close(r.stop)
		r.stop = nil
	}
	if r.lock != "" {
		os.Remove(r.lock)
		r.lock = ""
	}
}

func (r *Repo) local() yur.FileStore {
	return r.Store.(*lazyStore).local
}

func (r *Repo) headTree(b *Branch) yur.Hash {
	if b.Head.IsZero() {
		return yur.EmptyTree(r.Store)
	}
	rev, err := yur.ReadRev(r.Store, b.Head)
	if err != nil {
		return yur.EmptyTree(r.Store)
	}
	return rev.Tree
}

// What the files of a branch are: its unsaved changes, or its last rev
func (r *Repo) workTree(b *Branch) yur.Hash {
	if !b.Work.IsZero() {
		if rev, err := yur.ReadRev(r.Store, b.Work); err == nil {
			return rev.Tree
		}
	}
	return r.headTree(b)
}

func (r *Repo) workChange(b *Branch) string {
	if !b.Work.IsZero() {
		if rev, err := yur.ReadRev(r.Store, b.Work); err == nil {
			return rev.Change
		}
	}
	return ""
}

// Sets the unsaved changes of a branch from a tree: none when it is the head's own
func (r *Repo) setWork(b *Branch, tree yur.Hash) error {
	if tree == r.headTree(b) {
		b.Work = yur.Zero
		return nil
	}
	change := r.workChange(b)
	if change == "" {
		change = yur.NewChange()
	}
	rev := yur.Rev{Tree: tree, Change: change, Author: me(), Time: time.Now().Unix()}
	if !b.Head.IsZero() {
		rev.Parents = []yur.Hash{b.Head}
	}
	h, err := yur.WriteRev(r.Store, rev)
	if err != nil {
		return err
	}
	b.Work = h
	return nil
}

// Branches first, then change ids and hashes; @ is where the work is, @- the rev under it
func (r *Repo) resolve(name string) (yur.Hash, error) {
	b := r.State.current()
	switch {
	case name == "" || name == "@":
		if !b.Work.IsZero() {
			return b.Work, nil
		}
		return b.Head, nil
	case strings.HasPrefix(name, "@"):
		h, _ := r.resolve("@")
		for _, c := range name[1:] {
			if c != '-' {
				return yur.Zero, errors.New("unknown rev " + name)
			}
			rev, err := yur.ReadRev(r.Store, h)
			if err != nil || len(rev.Parents) == 0 {
				return yur.Zero, errors.New("no rev " + name)
			}
			h = rev.Parents[0]
		}
		return h, nil
	}
	if other, ok := r.State.Branches[name]; ok {
		return other.Head, nil
	}
	if h, ok := r.State.Tags[name]; ok {
		return h, nil
	}
	if r.State.Remote != nil {
		if h, ok := r.State.Remote.Branches[strings.TrimPrefix(name, "remote/")]; ok && strings.HasPrefix(name, "remote/") {
			return h, nil
		}
		if h, ok := r.State.Remote.Tags[strings.TrimPrefix(name, "remote/")]; ok {
			return h, nil
		}
	}
	name = strings.ToLower(name)
	if len(name) < 4 {
		return yur.Zero, errors.New("unknown rev " + name)
	}
	heads := []yur.Hash{}
	for _, br := range r.State.Branches {
		heads = append(heads, br.Head, br.Work)
	}
	if r.State.Remote != nil {
		for _, h := range r.State.Remote.Branches {
			heads = append(heads, h)
		}
	}
	for _, h := range r.State.Tags {
		heads = append(heads, h)
	}
	order, revs, err := yur.Ancestors(r.Store, heads, nil, 0)
	if err != nil {
		return yur.Zero, err
	}
	var found []yur.Hash
	for _, h := range order {
		if strings.HasPrefix(h.String(), name) {
			found = append(found, h)
		}
	}
	// A change id means its latest rev: the one of the work over the saved one. Its letters b c d f are
	// hex digits too, so a short one may fit a hash as well: then it fits several
	for _, h := range order {
		if strings.HasPrefix(revs[h].Change, name) {
			seen := false
			for _, f := range found {
				if revs[f].Change == revs[h].Change {
					seen = true
				}
			}
			if !seen {
				found = append(found, h)
			}
		}
	}
	switch len(found) {
	case 0:
		return yur.Zero, errors.New("unknown rev " + name)
	case 1:
		return found[0], nil
	}
	return yur.Zero, errors.New(name + " fits several revs, give more of it")
}

// The operation log: every state a command left, so any of them can be undone

type Op struct {
	ID      int    `json:"id"`
	Time    int64  `json:"time"`
	Command string `json:"command"`
	Before  *State `json:"before"`
	After   *State `json:"after"`
	// For undo and redo, the op they went back over
	Target int `json:"target,omitempty"`
}

func (r *Repo) ops() []Op {
	data, err := os.ReadFile(filepath.Join(r.Dir, "ops.jsonl"))
	if err != nil {
		return nil
	}
	list := []Op{}
	for _, line := range strings.Split(string(data), "\n") {
		var op Op
		if line != "" && json.Unmarshal([]byte(line), &op) == nil {
			list = append(list, op)
		}
	}
	return list
}

func (r *Repo) record(command string, target int) error {
	if stateEqual(r.before, r.State) {
		return nil
	}
	list := r.ops()
	id := 1
	if len(list) > 0 {
		id = list[len(list)-1].ID + 1
	}
	op := Op{ID: id, Time: time.Now().Unix(), Command: command, Before: r.before, After: r.State.clone(), Target: target}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(r.Dir, "ops.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	r.before = r.State.clone()
	return err
}

func stateEqual(a, b *State) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
