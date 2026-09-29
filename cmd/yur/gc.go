package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yurba.one/yur"
)

// Old operations are forgotten after this, and with them what only they could bring back
const keepOps = 30 * 24 * time.Hour

func cmdGc(r *Repo, a *args) error {
	list := r.ops()
	cut := time.Now().Add(-keepOps).Unix()
	kept := []Op{}
	for _, op := range list {
		if op.Time >= cut {
			kept = append(kept, op)
		}
	}
	states := []*State{r.State}
	for _, op := range kept {
		states = append(states, op.Before, op.After)
	}
	roots := []yur.Hash{}
	for _, s := range states {
		if s == nil {
			continue
		}
		for _, b := range s.Branches {
			roots = append(roots, b.Head, b.Work)
			if b.Merge != nil {
				roots = append(roots, b.Merge.Base, b.Merge.Ours, b.Merge.Theirs)
				if b.Merge.Parent != nil {
					roots = append(roots, *b.Merge.Parent)
				}
			}
		}
		for _, h := range s.Tags {
			roots = append(roots, h)
		}
		if s.Remote != nil {
			for _, h := range s.Remote.Branches {
				roots = append(roots, h)
			}
			for _, h := range s.Remote.Tags {
				roots = append(roots, h)
			}
		}
	}

	local := r.local()
	live := map[yur.Hash]bool{}
	var tree func(h yur.Hash)
	tree = func(h yur.Hash) {
		if live[h] {
			return
		}
		live[h] = true
		t, err := yur.ReadTree(local, h)
		if err != nil {
			return
		}
		for _, e := range t {
			if e.Kind == yur.KindDir {
				tree(e.Hash)
				continue
			}
			live[e.Hash] = true
			if local.Has(e.Hash) {
				parts, _ := yur.FileParts(local, e.Hash)
				for _, p := range parts {
					live[p] = true
				}
			}
		}
	}
	queue := []yur.Hash{}
	for _, h := range roots {
		if !h.IsZero() {
			queue = append(queue, h)
		}
	}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if live[h] {
			continue
		}
		kind, payload, err := yur.Read(local, h)
		if err != nil {
			continue
		}
		if kind == yur.TypeTree {
			tree(h)
			continue
		}
		live[h] = true
		if kind == yur.TypeRev {
			rev, err := yur.ParseRev(payload)
			if err != nil {
				continue
			}
			tree(rev.Tree)
			queue = append(queue, rev.Parents...)
		}
	}

	removed, freed := 0, int64(0)
	// Objects written in the last hour may belong to a command still running elsewhere
	young := time.Now().Add(-time.Hour)
	filepath.WalkDir(local.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(local.Dir, p)
		h, err := yur.ParseHash(strings.ReplaceAll(filepath.ToSlash(rel), "/", ""))
		if err != nil || live[h] {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(young) {
			freed += info.Size()
			os.Remove(p)
			removed++
		}
		return nil
	})

	var b strings.Builder
	for _, op := range kept {
		data, _ := json.Marshal(op)
		b.Write(append(data, '\n'))
	}
	os.WriteFile(filepath.Join(r.Dir, "ops.jsonl"), []byte(b.String()), 0644)
	fmt.Printf("Removed %d objects, %.1f MB\n", removed, float64(freed)/(1<<20))
	return nil
}
