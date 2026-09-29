package yur

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackStore(t *testing.T) {
	dir := t.TempDir()
	s, err := NewPackStore(filepath.Join(dir, "objects"), filepath.Join(dir, "packs"))
	if err != nil {
		t.Fatal(err)
	}
	var kept, dropped []Hash
	for i := 0; i < 20; i++ {
		h, err := Write(s, TypeBlob, []byte(strings.Repeat("x", i+1)))
		if err != nil {
			t.Fatal(err)
		}
		if i%4 == 0 {
			dropped = append(dropped, h)
		} else {
			kept = append(kept, h)
		}
	}
	keep := map[Hash]bool{}
	for _, h := range kept {
		keep[h] = true
	}
	all := append(append([]Hash{}, kept...), dropped...)
	n, err := s.Repack(func(h Hash) bool { return keep[h] }, all)
	if err != nil || n != len(all) {
		t.Fatalf("first pack: %d %v", n, err)
	}
	// Loose files are gone, the objects read from the pack
	for _, h := range all {
		if s.Loose.Has(h) {
			t.Fatalf("%s still loose", h.Short())
		}
		if _, data, err := Read(s, h); err != nil || len(data) == 0 {
			t.Fatalf("%s unreadable: %v", h.Short(), err)
		}
	}
	// A second repack leaves out what isn't kept any more
	if dead, total := s.Dead(func(h Hash) bool { return keep[h] }); dead != len(dropped) || total != len(all) {
		t.Fatalf("dead %d of %d", dead, total)
	}
	if n, err := s.Repack(func(h Hash) bool { return keep[h] }, nil); err != nil || n != len(kept) {
		t.Fatalf("second pack: %d %v", n, err)
	}
	for _, h := range dropped {
		if s.Has(h) {
			t.Fatalf("%s should be gone", h.Short())
		}
	}
	// A store opened afresh finds the pack by its index
	again, err := NewPackStore(filepath.Join(dir, "objects"), filepath.Join(dir, "packs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range kept {
		if _, _, err := Read(again, h); err != nil {
			t.Fatalf("reopened %s: %v", h.Short(), err)
		}
	}
	packs, _ := filepath.Glob(filepath.Join(dir, "packs", "*.pack"))
	count := 0
	if len(packs) != 1 || readPackFile(packs[0], func(Hash, []byte) { count++ }) != nil || count != len(kept) {
		t.Fatalf("packs on disk: %v, %d objects", packs, count)
	}
}

func TestBlame(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	commit := func(parent Hash, content string) Hash {
		f, _ := WriteFile(s, []byte(content))
		tree, _ := Edit(s, EmptyTree(s), map[string]*Entry{"a.txt": {Kind: KindFile, Hash: f, Size: int64(len(content))}})
		rev := Rev{Tree: tree, Change: NewChange(), Message: content}
		if !parent.IsZero() {
			rev.Parents = []Hash{parent}
		}
		h, _ := WriteRev(s, rev)
		return h
	}
	first := commit(Zero, "one\ntwo\nthree\n")
	second := commit(first, "one\nTWO\nthree\n")
	third := commit(second, "one\nTWO\nthree\nfour\n")
	lines, owners, revs, err := Blame(s, third, "a.txt", 100)
	if err != nil || len(lines) != 4 {
		t.Fatalf("%v %d lines", err, len(lines))
	}
	want := []Hash{first, second, first, third}
	for i, w := range want {
		if owners[i] < 0 || revs[owners[i]] != w {
			t.Fatalf("line %d: want %s", i+1, w.Short())
		}
	}
}

// The objects of a pack file, read back with ReadPack into a store that only counts them
func readPackFile(path string, each func(h Hash, compressed []byte)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = ReadPack(bytes.NewReader(data), counter(each), nil)
	return err
}

type counter func(h Hash, compressed []byte)

func (c counter) Has(Hash) bool                   { return false }
func (c counter) ReadRaw(Hash) ([]byte, error)    { return nil, os.ErrNotExist }
func (c counter) WriteRaw(h Hash, b []byte) error { c(h, b); return nil }
