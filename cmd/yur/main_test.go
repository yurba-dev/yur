package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseArgs(t *testing.T) {
	a := parseArgs([]string{"-b", "dev", "folder", "--force=false", "-m", "a message", "--", "-not-a-flag"})
	if a.get("b") != "dev" || a.arg(0) != "folder" || a.arg(1) != "-not-a-flag" {
		t.Fatalf("values: %+v", a)
	}
	if a.bool("force") {
		t.Fatal("--force=false is off")
	}
	if a.get("m") != "a message" {
		t.Fatal("-m takes the next word")
	}
}

func TestIgnoreRules(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\nbuild/\n"), 0644)
	os.WriteFile(filepath.Join(root, ".yurignore"), []byte("!keep.log\nsecret.txt\n"), 0644)
	ig := newIgnorer(root)
	cases := map[string]bool{"a.log": true, "keep.log": false, "secret.txt": true, "src/main.go": false, "node_modules": true}
	for p, want := range cases {
		dir := p == "node_modules"
		if ig.ignored(p, dir) != want {
			t.Fatalf("%s: want ignored %v", p, want)
		}
	}
	if !ig.ignored("build", true) || ig.ignored("build", false) {
		t.Fatal("build/ is a folder rule")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.1.0", "1.0.9", true}, {"1.0.0", "1.0.0", false}, {"1.10.0", "1.9.0", true}, {"2", "1.99", true}, {"1.0", "1.0.1", false}} {
		if newer(c.a, c.b) != c.want {
			t.Fatalf("newer(%s, %s)", c.a, c.b)
		}
	}
}

func TestGitQuote(t *testing.T) {
	if gitQuote("a b/c.txt") != "a b/c.txt" || gitQuote(`a"b`) != `"a\"b"` || gitQuote("x\ny") != `"x\ny"` {
		t.Fatal("paths git would misread are quoted, the rest left as they are")
	}
}

func TestCompletionCandidates(t *testing.T) {
	got := candidates(nil, "")
	found := map[string]bool{}
	for _, c := range got {
		found[c] = true
	}
	for _, want := range []string{"sync", "cherry-pick", "export-git"} {
		if !found[want] {
			t.Fatalf("no %s among %v", want, got)
		}
	}
	if list := candidates([]string{"check", "build"}, ""); len(list) != 4 {
		t.Fatalf("check states: %v", list)
	}
}
