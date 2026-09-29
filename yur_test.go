package yur

import (
	"bytes"
	"strings"
	"testing"
)

func lines(s string) []string { return SplitLines([]byte(s)) }

func TestDiff(t *testing.T) {
	a := lines("a\nb\nc\nd\ne\n")
	b := lines("a\nB\nc\nd\ne\nf\n")
	hunks, added, removed := Hunks(a, b, 1)
	if added != 2 || removed != 1 || len(hunks) != 2 {
		t.Fatalf("added %d removed %d hunks %d\n%s", added, removed, len(hunks), Unified(hunks))
	}
}

func TestMerge3(t *testing.T) {
	base := lines("one\ntwo\nthree\nfour\nfive\n")
	ours := lines("ONE\ntwo\nthree\nfour\nfive\n")
	theirs := lines("one\ntwo\nthree\nfour\nFIVE\n")
	merged, ok := Merge3(base, ours, theirs, "ours", "theirs")
	if !ok || strings.Join(merged, "") != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("clean merge: %v %q", ok, strings.Join(merged, ""))
	}
	theirs = lines("uno\ntwo\nthree\nfour\nfive\n")
	merged, ok = Merge3(base, ours, theirs, "ours", "theirs")
	if ok || !HasMarkers([]byte(strings.Join(merged, ""))) {
		t.Fatalf("conflict expected: %q", strings.Join(merged, ""))
	}
	same, ok := Merge3(base, ours, ours, "ours", "theirs")
	if !ok || strings.Join(same, "") != strings.Join(ours, "") {
		t.Fatalf("same change twice: %q", strings.Join(same, ""))
	}
}

func TestTreesAndPack(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	file := func(text string) *Entry {
		h, _ := WriteFile(s, []byte(text))
		return &Entry{Kind: KindFile, Hash: h, Size: int64(len(text))}
	}
	base, err := Edit(s, Zero, map[string]*Entry{"README.md": file("hello\n"), "src/main.go": file("package main\n"), "src/util/a.go": file("a\n")})
	if err != nil {
		t.Fatal(err)
	}
	ours, _ := Edit(s, base, map[string]*Entry{"README.md": file("hello world\n")})
	theirs, _ := Edit(s, base, map[string]*Entry{"src/util/a.go": nil, "src/new.go": file("new\n")})
	merged, conflicts, err := MergeTrees(s, base, ours, theirs, "ours", "theirs")
	if err != nil || len(conflicts) != 0 {
		t.Fatal(err, conflicts)
	}
	files, _ := Flatten(s, merged)
	if len(files) != 3 || files["src/new.go"].Size != 4 {
		t.Fatalf("merged files %v", files)
	}
	if _, err := Lookup(s, merged, "src/util"); err == nil {
		t.Fatal("empty folder kept")
	}
	changes, _ := DiffTrees(s, base, merged)
	if len(changes) != 3 {
		t.Fatalf("changes %v", changes)
	}

	r1, _ := WriteRev(s, Rev{Tree: base, Change: NewChange(), Author: Author{1, "A"}, Time: 1, Message: "first"})
	r2, _ := WriteRev(s, Rev{Tree: merged, Parents: []Hash{r1}, Change: NewChange(), Author: Author{1, "A"}, Time: 2, Message: "second\n\nbody"})
	got, _ := ReadRev(s, r2)
	if got.Title() != "second" || got.Parents[0] != r1 {
		t.Fatalf("rev %+v", got)
	}
	if b, _ := MergeBase(s, r2, r1); b != r1 || !IsAncestor(s, r1, r2) {
		t.Fatal("ancestry")
	}

	need, err := Needed(s, []Hash{r2}, []Hash{r1}, true)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WritePack(&buf, s, need); err != nil {
		t.Fatal(err)
	}
	other := FileStore{Dir: t.TempDir()}
	all, _ := Needed(s, []Hash{r1}, nil, true)
	var first bytes.Buffer
	WritePack(&first, s, all)
	ReadPack(&first, other, nil)
	if _, err := ReadPack(&buf, other, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Flatten(other, merged); err != nil {
		t.Fatal("pack incomplete: ", err)
	}
}

func TestChunks(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	data := make([]byte, 9<<20)
	x := uint32(1)
	for i := range data {
		x = x*1664525 + 1013904223
		data[i] = byte(x >> 24)
	}
	h, _ := WriteFile(s, data)
	back, err := ReadFile(s, h)
	if err != nil || !bytes.Equal(back, data) {
		t.Fatal("chunked file changed", err)
	}
	parts, _ := FileParts(s, h)
	if len(parts) < 2 {
		t.Fatal("not chunked")
	}
}

func TestSecrets(t *testing.T) {
	// Put together here, so the test file itself holds no key a scan would stop
	key := "AKIA" + strings.Repeat("ABCD", 4)
	if len(FindSecrets("config.js", []byte("const key = '"+key+"'\n"))) != 1 {
		t.Fatal("aws key missed")
	}
	if len(FindSecrets(".env.example", []byte("KEY="+key))) != 0 {
		t.Fatal("sample flagged")
	}
}
