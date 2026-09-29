// Package yur is the version control of Yurba Projects: the objects a project is made of and what is done
// with them. The command line (cmd/yur) and the Yurba server share it, so both read and write the same format.
package yur

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Hash [32]byte

var Zero Hash

func (h Hash) String() string { return hex.EncodeToString(h[:]) }
func (h Hash) Short() string  { return h.String()[:10] }
func (h Hash) IsZero() bool   { return h == Zero }

// In JSON a hash is its hex, and no hash at all an empty string
func (h Hash) MarshalText() ([]byte, error) {
	if h.IsZero() {
		return []byte{}, nil
	}
	return []byte(h.String()), nil
}

func (h *Hash) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*h = Zero
		return nil
	}
	parsed, err := ParseHash(string(text))
	*h = parsed
	return err
}

func ParseHash(s string) (Hash, error) {
	var h Hash
	if len(s) != 64 {
		return h, errors.New("bad_hash")
	}
	_, err := hex.Decode(h[:], []byte(s))
	return h, err
}

const (
	TypeBlob   = "blob"
	TypeChunks = "chunks"
	TypeTree   = "tree"
	TypeRev    = "rev"
)

// An object is hashed with its type and length in front, so a blob can never pass for a tree of the same bytes
func Encode(kind string, payload []byte) []byte {
	head := kind + " " + strconv.Itoa(len(payload)) + "\x00"
	out := make([]byte, 0, len(head)+len(payload))
	out = append(out, head...)
	return append(out, payload...)
}

func HashOf(raw []byte) Hash { return sha256.Sum256(raw) }

func Decode(raw []byte) (string, []byte, error) {
	i := bytes.IndexByte(raw, 0)
	if i < 0 {
		return "", nil, errors.New("bad_object")
	}
	kind, size, ok := strings.Cut(string(raw[:i]), " ")
	n, err := strconv.Atoi(size)
	if !ok || err != nil || n != len(raw)-i-1 {
		return "", nil, errors.New("bad_object")
	}
	return kind, raw[i+1:], nil
}

// Tree

const (
	KindFile = "f"
	KindExec = "x"
	KindLink = "l"
	KindDir  = "d"
)

type Entry struct {
	Kind string
	Hash Hash
	// The file's length, so a listing needs no blob: a light clone and the web show sizes without them
	Size int64
	Name string
}

type Tree []Entry

// .yur and .git are the folders of the version controls themselves, never a file of a project: a tree
// holding one would write into them on checkout. In any case and with the dots and spaces Windows drops
// from the end of a name, as a disk would take ".GIT." for .git.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\n\r\x00") {
		return false
	}
	bare := strings.TrimRight(name, ". ")
	return !strings.EqualFold(bare, ".yur") && !strings.EqualFold(bare, ".git")
}

func (t Tree) Encode() []byte {
	sorted := append(Tree(nil), t...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b bytes.Buffer
	for _, e := range sorted {
		fmt.Fprintf(&b, "%s %s %d %s\n", e.Kind, e.Hash, e.Size, e.Name)
	}
	return b.Bytes()
}

func ParseTree(payload []byte) (Tree, error) {
	tree := Tree{}
	for _, line := range strings.Split(string(payload), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 4)
		if len(parts) != 4 {
			return nil, errors.New("bad_tree")
		}
		h, err := ParseHash(parts[1])
		size, err2 := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || err2 != nil || size < 0 || !ValidName(parts[3]) || !validKind(parts[0]) {
			return nil, errors.New("bad_tree")
		}
		// Names come sorted and once each, as Encode writes them: a second entry of a name would be seen
		// by one reader and not by another
		if n := len(tree); n > 0 && tree[n-1].Name >= parts[3] {
			return nil, errors.New("bad_tree")
		}
		tree = append(tree, Entry{Kind: parts[0], Hash: h, Size: size, Name: parts[3]})
	}
	return tree, nil
}

// By name, for walks over two trees at once: Find on each name would make a big folder quadratic
func (t Tree) byName() map[string]Entry {
	m := make(map[string]Entry, len(t))
	for _, e := range t {
		m[e.Name] = e
	}
	return m
}

func validKind(kind string) bool {
	return kind == KindFile || kind == KindExec || kind == KindLink || kind == KindDir
}

// For a tree as ReadTree gives it: sorted by name, which ParseTree makes sure of
func (t Tree) Find(name string) (Entry, bool) {
	i := sort.Search(len(t), func(i int) bool { return t[i].Name >= name })
	if i < len(t) && t[i].Name == name {
		return t[i], true
	}
	return Entry{}, false
}

// Rev

type Author struct {
	ID   int
	Name string
}

// A saved state of the project. Change stays the same when a rev is amended or moved onto another
// base, so a change is followed through every rewrite, which a hash alone can't do.
type Rev struct {
	Tree      Hash
	Parents   []Hash
	Change    string
	Author    Author
	Time      int64
	Conflicts []string
	Message   string
}

func (r Rev) Encode() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "tree %s\n", r.Tree)
	for _, p := range r.Parents {
		fmt.Fprintf(&b, "parent %s\n", p)
	}
	fmt.Fprintf(&b, "change %s\n", r.Change)
	fmt.Fprintf(&b, "author %d %s\n", r.Author.ID, strings.ReplaceAll(r.Author.Name, "\n", " "))
	fmt.Fprintf(&b, "time %d\n", r.Time)
	for _, c := range r.Conflicts {
		fmt.Fprintf(&b, "conflict %s\n", c)
	}
	b.WriteString("\n")
	b.WriteString(r.Message)
	return b.Bytes()
}

func ParseRev(payload []byte) (Rev, error) {
	var r Rev
	head, message, _ := strings.Cut(string(payload), "\n\n")
	r.Message = message
	for _, line := range strings.Split(head, "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "tree":
			h, err := ParseHash(value)
			if err != nil {
				return r, errors.New("bad_rev")
			}
			r.Tree = h
		case "parent":
			h, err := ParseHash(value)
			if err != nil {
				return r, errors.New("bad_rev")
			}
			r.Parents = append(r.Parents, h)
		case "change":
			r.Change = value
		case "author":
			id, name, _ := strings.Cut(value, " ")
			r.Author.ID, _ = strconv.Atoi(id)
			r.Author.Name = name
		case "time":
			r.Time, _ = strconv.ParseInt(value, 10, 64)
		case "conflict":
			r.Conflicts = append(r.Conflicts, value)
		}
	}
	if r.Tree.IsZero() {
		return r, errors.New("bad_rev")
	}
	return r, nil
}

// The first line of the message, what a list shows
func (r Rev) Title() string {
	title, _, _ := strings.Cut(strings.TrimSpace(r.Message), "\n")
	return title
}

// Consonants only: a change id never looks like a hash, so the two are never mistaken for each other
const changeAlphabet = "bcdfghjklmnpqrstvwxz"

func ValidChange(s string) bool {
	if len(s) < 4 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(changeAlphabet, c) {
			return false
		}
	}
	return true
}

// Names of branches and releases: they go into addresses and file names of a clone
func ValidRef(name string) bool {
	if name == "" || len(name) > 100 || strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, "/") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_./+", c)) {
			return false
		}
	}
	return true
}
