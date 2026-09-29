package yur

import (
	"sort"
	"strings"
	"testing"
)

func file(t *testing.T, s Store, content string) *Entry {
	h, err := WriteFile(s, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return &Entry{Kind: KindFile, Hash: h, Size: int64(len(content))}
}

func tree(t *testing.T, s Store, files map[string]string) Hash {
	edits := map[string]*Entry{}
	for p, c := range files {
		edits[p] = file(t, s, c)
	}
	h, err := Edit(s, EmptyTree(s), edits)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func paths(t *testing.T, s Store, h Hash) string {
	files, err := Flatten(s, h)
	if err != nil {
		t.Fatal(err)
	}
	list := []string{}
	for p := range files {
		list = append(list, p)
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

func TestEditUnderFile(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	root := tree(t, s, map[string]string{"a": "x"})
	h, err := Edit(s, root, map[string]*Entry{"a/b": nil})
	if err != nil || paths(t, s, h) != "a" {
		t.Fatalf("the file under the removal must stay: %v %q", err, paths(t, s, h))
	}
}

func TestMergeFolderAgainstFile(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	base := tree(t, s, map[string]string{"x": "1"})
	ours := tree(t, s, map[string]string{"x": "1", "a/b": "ours"})
	theirs := tree(t, s, map[string]string{"x": "1", "a": "theirs"})
	merged, conflicts, err := MergeTrees(s, base, ours, theirs, "ours", "theirs")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "a" || paths(t, s, merged) != "a/b,x" {
		t.Fatalf("folder vs file: %v %v %q", err, conflicts, paths(t, s, merged))
	}
	merged, conflicts, err = MergeTrees(s, base, theirs, ours, "ours", "theirs")
	if err != nil || len(conflicts) != 1 || paths(t, s, merged) != "a,x" {
		t.Fatalf("file vs folder: %v %v %q", err, conflicts, paths(t, s, merged))
	}
}

func TestMerge3TouchingAfterLongerChange(t *testing.T) {
	base := lines("1\n2\n3\n4\n5\n6\n7\n")
	ours := lines("1\nA\nB\nC\nD\n6\n7\n")
	theirs := lines("1\n2\nX\n4\n5\nY\n7\n")
	_, ok := Merge3(base, ours, theirs, "ours", "theirs")
	if ok {
		t.Fatal("a change touching the end of the other side's must be part of the conflict")
	}
}

func TestMergeBaseWithSkewedClock(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	root := tree(t, s, map[string]string{"f": "1"})
	rev := func(when int64, parents ...Hash) Hash {
		h, err := WriteRev(s, Rev{Tree: root, Parents: parents, Change: NewChange(), Time: when, Message: "m"})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	x := rev(100)
	y := rev(50, x)
	a := rev(300, y)
	z := rev(200, x)
	b := rev(400, y, z)
	base, err := MergeBase(s, a, b)
	if err != nil || base != y {
		t.Fatalf("want the nearer base y, got %v %v", base.Short(), err)
	}
	order, revs, _ := Ancestors(s, []Hash{a, b}, nil, 0)
	at := map[Hash]int{}
	for i, h := range Topo(order, revs) {
		at[h] = i
	}
	for h, r := range revs {
		for _, p := range r.Parents {
			if at[p] > at[h] {
				t.Fatalf("%s comes before its parent %s", h.Short(), p.Short())
			}
		}
	}
}

func TestSecretsByName(t *testing.T) {
	key := "AKIA" + strings.Repeat("ABCD", 4)
	for _, p := range []string{"templates/deploy.js", "samples/aws.js"} {
		if len(FindSecrets(p, []byte("key = "+key+"\n"))) == 0 {
			t.Fatalf("%s: a folder's name must not hide a key", p)
		}
	}
	if len(FindSecrets("config/.env.example", []byte("KEY="+key+"\n"))) != 0 {
		t.Fatal("a sample file is left alone")
	}
	if !SecretFile("app/.env.staging") || SecretFile(".env.example") {
		t.Fatal(".env.* files are secrets, their samples not")
	}
	openai := "sk-proj-" + strings.Repeat("aB3_", 12)
	if len(FindSecrets("main.py", []byte("k = \""+openai+"\"\n"))) == 0 {
		t.Fatal("a newer OpenAI key with _ in it")
	}
}

func TestHunkHeadersWithoutContext(t *testing.T) {
	hunks, _, _ := Hunks(lines("a\nb\nc\n"), lines("a\nb\nX\nc\n"), 0)
	if len(hunks) != 1 || hunks[0].OldStart != 2 || hunks[0].OldLines != 0 || hunks[0].NewStart != 3 {
		t.Fatalf("an insertion sits after the line before it: %+v", hunks)
	}
}

func TestForgedObjectIsRefused(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	good := file(t, s, "real content")
	junk := Deflate(Encode(TypeBlob, []byte("something else")))
	if err := s.WriteRaw(good.Hash, junk); err == nil {
		t.Fatal("bytes that don't match the hash must be refused even when the object is here")
	}
	if _, err := Inflate(append(Deflate(Encode(TypeBlob, []byte("x"))), 1, 2, 3)); err == nil {
		t.Fatal("bytes after the stream must be refused")
	}
}

func TestChunkListCannotBlowUp(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	part, _ := Write(s, TypeBlob, []byte(strings.Repeat("0", 1<<20)))
	list := strings.Repeat(part.String()+"\n", 64)
	h, _ := Write(s, TypeChunks, []byte(list))
	if _, err := ReadFileMax(s, h, 8<<20); err == nil || err.Error() != "file_too_large" {
		t.Fatalf("64 MB of chunks past an 8 MB limit: %v", err)
	}
}

func TestReservedNames(t *testing.T) {
	for _, name := range []string{".yur", ".YUR", ".git", ".Git.", ".git "} {
		if ValidName(name) {
			t.Fatalf("%q must not be a name in a tree", name)
		}
	}
	for _, name := range []string{".gitignore", ".github", "yur", "a.git"} {
		if !ValidName(name) {
			t.Fatalf("%q is an ordinary name", name)
		}
	}
}
