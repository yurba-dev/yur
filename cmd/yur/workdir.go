package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"yurba.one/yur"
)

// Ignore rules

type rule struct {
	re     *regexp.Regexp
	negate bool
	dir    bool
}

type ignorer struct {
	// Rules by the folder their file sits in, "" for the root
	rules map[string][]rule
}

// Left out even without an ignore file: no one means to keep these
var defaultIgnores = []string{".git/", ".yur/", ".DS_Store", "Thumbs.db", "desktop.ini", "node_modules/", "__pycache__/", "*.pyc", "*.swp", "*~"}

func globRegexp(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			i++
			if i+1 < len(pattern) && pattern[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := pattern[i+1 : i+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + class + "]")
			i += end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

func parseRules(text string) []rule {
	rules := []rule{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := rule{}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dir = true
			line = strings.TrimSuffix(line, "/")
		}
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		prefix := "(?:.*/)?"
		if anchored {
			prefix = ""
		}
		re, err := regexp.Compile("^" + prefix + globRegexp(line) + "$")
		if err == nil {
			r.re = re
			rules = append(rules, r)
		}
	}
	return rules
}

func newIgnorer(root string) *ignorer {
	ig := &ignorer{rules: map[string][]rule{"": parseRules(strings.Join(defaultIgnores, "\n"))}}
	ig.load(root, "")
	return ig
}

// A folder's .gitignore and then its .yurignore, so a project from git keeps its rules and yur's own
// come after them, the last word
func (ig *ignorer) load(root, dir string) {
	for _, name := range []string{".gitignore", ".yurignore"} {
		if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name)); err == nil {
			ig.rules[dir] = append(ig.rules[dir], parseRules(string(data))...)
		}
	}
}

func (ig *ignorer) ignored(p string, isDir bool) bool {
	result := false
	dirs := []string{""}
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		dirs = append(dirs, strings.Join(parts[:i], "/"))
	}
	for _, dir := range dirs {
		rel := p
		if dir != "" {
			rel = strings.TrimPrefix(p, dir+"/")
		}
		for _, r := range ig.rules[dir] {
			if r.dir && !isDir {
				continue
			}
			if r.re.MatchString(rel) {
				result = !r.negate
			}
		}
	}
	return result
}

// Snapshot

type indexEntry struct {
	Size  int64    `json:"s"`
	Mtime int64    `json:"m"`
	Hash  yur.Hash `json:"h"`
	Kind  string   `json:"k"`
}

func (r *Repo) loadIndex() map[string]indexEntry {
	index := map[string]indexEntry{}
	if data, err := os.ReadFile(filepath.Join(r.Dir, "index.json")); err == nil {
		json.Unmarshal(data, &index)
	}
	return index
}

func (r *Repo) saveIndex(index map[string]indexEntry) {
	data, _ := json.Marshal(index)
	os.WriteFile(filepath.Join(r.Dir, "index.json"), data, 0644)
}

// Reads the folder as it is into objects and gives its tree. Files unchanged since the last look (same
// size and time) are not read again.
func (r *Repo) scan() (yur.Hash, error) {
	index := r.loadIndex()
	fresh := map[string]indexEntry{}
	ig := newIgnorer(r.Root)
	known, _ := yur.Flatten(r.Store, r.workTree(r.State.current()))
	// A file the project holds stays in it when a rule would leave it out, as in git: only new ones are ignored
	knownDirs := map[string]bool{}
	for p := range known {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			knownDirs[d] = true
		}
	}
	files := map[string]yur.Entry{}
	// Files this system can't hold (con.txt on Windows) were never written: they stay as they are
	for p, entry := range known {
		if unrepresentable(p) {
			files[p] = entry
		}
	}
	// A file changed in the same second the index was written might keep its time, so it is read again
	recent := time.Now().Add(-2 * time.Second).UnixNano()

	err := filepath.WalkDir(r.Root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(r.Root, full)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if interrupted() {
			return errInterrupted
		}
		if d.IsDir() {
			if !yur.ValidName(d.Name()) || ig.ignored(rel, true) && !knownDirs[rel] {
				return filepath.SkipDir
			}
			ig.load(r.Root, rel)
			return nil
		}
		_, tracked := known[rel]
		if !yur.ValidName(d.Name()) || ig.ignored(rel, false) && !tracked {
			return nil
		}
		// One that can't be read now (locked, no rights) is kept as it was, not taken for removed
		keep := func() error {
			if old, ok := known[rel]; ok {
				files[rel] = old
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return keep()
		}
		kind := yur.KindFile
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			kind = yur.KindLink
		case !info.Mode().IsRegular():
			return nil
		case runtime.GOOS == "windows":
			// Windows keeps no executable bit, and a link was written as a file of its target: both stay
			if old, ok := known[rel]; ok && (old.Kind == yur.KindExec || old.Kind == yur.KindLink) {
				kind = old.Kind
			}
		case info.Mode()&0111 != 0:
			kind = yur.KindExec
		}
		mtime := info.ModTime().UnixNano()
		if cached, ok := index[rel]; ok && cached.Size == info.Size() && cached.Mtime == mtime && mtime < recent && cached.Kind == kind && r.Store.Has(cached.Hash) {
			fresh[rel] = cached
			files[rel] = yur.Entry{Kind: kind, Hash: cached.Hash, Size: cached.Size}
			return nil
		}
		var data []byte
		if kind == yur.KindLink && info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return keep()
			}
			data = []byte(filepath.ToSlash(target))
		} else if data, err = os.ReadFile(full); err != nil {
			return keep()
		}
		h, err := yur.WriteFile(r.Store, data)
		if err != nil {
			return err
		}
		fresh[rel] = indexEntry{Size: info.Size(), Mtime: mtime, Hash: h, Kind: kind}
		files[rel] = yur.Entry{Kind: kind, Hash: h, Size: int64(len(data))}
		return nil
	})
	if err != nil {
		return yur.Zero, err
	}
	r.saveIndex(fresh)
	return buildTree(r.Store, files)
}

func buildTree(s yur.Store, files map[string]yur.Entry) (yur.Hash, error) {
	here := yur.Tree{}
	below := map[string]map[string]yur.Entry{}
	for p, e := range files {
		name, rest, deeper := strings.Cut(p, "/")
		if !deeper {
			e.Name = name
			here = append(here, e)
			continue
		}
		if below[name] == nil {
			below[name] = map[string]yur.Entry{}
		}
		below[name][rest] = e
	}
	for name, sub := range below {
		h, err := buildTree(s, sub)
		if err != nil {
			return yur.Zero, err
		}
		here = append(here, yur.Entry{Kind: yur.KindDir, Hash: h, Name: name})
	}
	return yur.WriteTree(s, here)
}

// Takes in whatever changed in the folder as the current branch's unsaved work. Every command starts here.
func (r *Repo) snapshot() error {
	tree, err := r.scan()
	if err != nil {
		return err
	}
	b := r.State.current()
	if tree == r.workTree(b) {
		return nil
	}
	return r.setWork(b, tree)
}

// Checkout

// Turns the folder from one tree into another. The folder is taken to match from (a snapshot comes first).
func (r *Repo) checkout(from, to yur.Hash) error {
	changes, err := yur.DiffTrees(r.Store, from, to)
	if err != nil {
		return err
	}
	missing := []yur.Hash{}
	for _, c := range changes {
		if c.New != nil && !r.Store.Has(c.New.Hash) {
			missing = append(missing, c.New.Hash)
		}
	}
	if len(missing) > 0 {
		if err := r.fetchObjects(missing); err != nil {
			return err
		}
	}

	index := r.loadIndex()
	if err := r.checkNames(changes, index); err != nil {
		return err
	}
	dirs := map[string]bool{}
	skipped := []string{}
	for _, c := range changes {
		if interrupted() {
			r.saveIndex(index)
			return errInterrupted
		}
		if unrepresentable(c.Path) {
			if c.New != nil {
				skipped = append(skipped, c.Path)
			}
			continue
		}
		full := filepath.Join(r.Root, filepath.FromSlash(c.Path))
		// Never through a link: a folder on the way that is one could lead out of the project
		if err := r.noLinkAbove(c.Path); err != nil {
			return err
		}
		if c.New == nil {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				r.saveIndex(index)
				return errors.New("can't remove " + c.Path + ": " + err.Error())
			}
			delete(index, c.Path)
			dirs[path.Dir(c.Path)] = true
			continue
		}
		data, err := yur.ReadFile(r.Store, c.New.Hash)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(full), 0755)
		if info, err := os.Lstat(full); err == nil && (info.IsDir() || info.Mode()&fs.ModeSymlink != 0) {
			os.RemoveAll(full)
		}
		if c.New.Kind == yur.KindLink && runtime.GOOS != "windows" {
			os.Remove(full)
			if err := os.Symlink(string(data), full); err != nil {
				return err
			}
		} else {
			mode := fs.FileMode(0644)
			if c.New.Kind == yur.KindExec {
				mode = 0755
			}
			if err := os.WriteFile(full, data, mode); err != nil {
				return err
			}
			if runtime.GOOS != "windows" {
				os.Chmod(full, mode)
			}
		}
		if info, err := os.Lstat(full); err == nil {
			index[c.Path] = indexEntry{Size: info.Size(), Mtime: info.ModTime().UnixNano(), Hash: c.New.Hash, Kind: c.New.Kind}
		}
	}
	// Folders a removal left empty go too, deepest first
	list := []string{}
	for dir := range dirs {
		for dir != "." && dir != "" {
			list = append(list, dir)
			dir = path.Dir(dir)
		}
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for _, dir := range list {
		os.Remove(filepath.Join(r.Root, filepath.FromSlash(dir)))
	}
	r.saveIndex(index)
	if len(skipped) > 0 {
		fmt.Println(paint(yellow, "Left out, as "+runtime.GOOS+" can't hold these names (they stay in the project):"))
		for _, p := range skipped {
			fmt.Println("  " + p)
		}
	}
	return nil
}

// A name Windows refuses or takes for a device: never written there, and kept as it is by a snapshot
var windowsReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)

func unrepresentable(p string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	for _, name := range strings.Split(p, "/") {
		if windowsReserved.MatchString(name) || strings.ContainsAny(name, "<>:\"|?*") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			return true
		}
		for _, c := range name {
			if c < 32 {
				return true
			}
		}
	}
	return false
}

// Two paths the disk would take for one (README and readme on Windows and macOS): writing the second
// would overwrite the first, or write through a link the first made
func (r *Repo) checkNames(changes []yur.FileChange, index map[string]indexEntry) error {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return nil
	}
	seen := map[string]string{}
	add := func(p string) error {
		parts := strings.Split(p, "/")
		for i := range parts {
			whole := strings.Join(parts[:i+1], "/")
			key := strings.ToLower(whole)
			if other, ok := seen[key]; ok && other != whole {
				return errors.New(other + " and " + whole + " differ only by case, which this disk can't hold apart")
			}
			seen[key] = whole
		}
		return nil
	}
	removed := map[string]bool{}
	for _, c := range changes {
		if c.New == nil {
			removed[c.Path] = true
		}
	}
	for p := range index {
		if !removed[p] {
			if err := add(p); err != nil {
				return err
			}
		}
	}
	for _, c := range changes {
		if c.New != nil {
			if err := add(c.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Repo) noLinkAbove(p string) error {
	dir := r.Root
	parts := strings.Split(p, "/")
	for _, name := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, name)
		if info, err := os.Lstat(dir); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("won't write " + p + " through the link " + name)
		}
	}
	return nil
}

// Moves the folder to what a branch holds, after the state already says so
func (r *Repo) showBranch(from yur.Hash) error {
	return r.checkout(from, r.workTree(r.State.current()))
}
