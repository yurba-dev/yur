package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yurba.one/yur"
)

func (r *Repo) needRemote() (*Remote, error) {
	if r.State.Remote == nil || r.State.Remote.Project == "" {
		return nil, errors.New("the project isn't on Yurba yet: yur create puts it there, yur remote owner/project links it")
	}
	return r.State.Remote, nil
}

// Asks the server where its branches are and brings the revs this clone lacks
func (r *Repo) refresh() (RefsModel, error) {
	rm, err := r.needRemote()
	if err != nil {
		return RefsModel{}, err
	}
	refs, err := fetchRefs(rm.Project)
	if err != nil {
		return refs, err
	}
	want := []yur.Hash{}
	for _, list := range []map[string]yur.Hash{refs.Branches, refs.Tags} {
		for _, h := range list {
			if !r.local().Has(h) {
				want = append(want, h)
			}
		}
	}
	if len(want) > 0 {
		have := []yur.Hash{}
		for _, br := range r.State.Branches {
			if !br.Head.IsZero() {
				have = append(have, br.Head)
			}
		}
		for _, h := range rm.Branches {
			if r.local().Has(h) {
				have = append(have, h)
			}
		}
		if _, err := r.fetch(want, have, !rm.Light, nil); err != nil {
			return refs, err
		}
	}
	// A branch the server had and has no more was removed there: sync leaves it alone, yur push puts it back
	for name := range rm.Branches {
		if _, still := refs.Branches[name]; !still && !contains(rm.Gone, name) {
			rm.Gone = append(rm.Gone, name)
		}
	}
	gone := []string{}
	for _, name := range rm.Gone {
		if _, back := refs.Branches[name]; !back {
			gone = append(gone, name)
		}
	}
	rm.Gone = gone
	rm.Branches = refs.Branches
	if rm.Branches == nil {
		rm.Branches = map[string]yur.Hash{}
	}
	rm.Default = refs.Default
	rm.Tags = refs.Tags
	return refs, nil
}

// Puts the server's head of the current branch under the local work: moving forward, or setting the
// local revs on top of the server's (rebase), or merging with --merge
func (r *Repo) integrate(remote yur.Hash, merge bool) error {
	b := r.State.current()
	if remote.IsZero() || remote == b.Head || yur.IsAncestor(r.Store, remote, b.Head) {
		return nil
	}
	oldHead := r.headTree(b)
	oldWork := r.workTree(b)
	serverTree := r.revTree(remote)

	if b.Head.IsZero() || yur.IsAncestor(r.Store, b.Head, remote) {
		_, behind := r.aheadBehind(b.Head, remote)
		b.Head = remote
		fmt.Printf("Brought %s from the server\n", plural(behind, "rev"))
		return r.carryWork(b, oldHead, oldWork, serverTree)
	}
	if merge {
		return r.mergeInto(b, "the server", remote, "Merge the server's "+r.State.Branch)
	}

	local, revs, err := yur.Only(r.Store, []yur.Hash{b.Head}, []yur.Hash{remote}, 0)
	if err != nil {
		return err
	}
	for _, h := range local {
		if len(revs[h].Parents) > 1 {
			return r.mergeInto(b, "the server", remote, "Merge the server's "+r.State.Branch)
		}
	}
	parent, parentTree := remote, serverTree
	var conflicts []string
	for i := len(local) - 1; i >= 0; i-- {
		rev := revs[local[i]]
		before := yur.EmptyTree(r.Store)
		if len(rev.Parents) > 0 {
			before = r.revTree(rev.Parents[0])
		}
		tree, c, err := yur.MergeTrees(r.Store, before, parentTree, rev.Tree, "server", "yours")
		if err != nil {
			return err
		}
		// Markers a rev left are carried by every rev on top of it: the rev keeps its own, the whole
		// rebase keeps them all
		rev.Tree, rev.Parents, rev.Conflicts = tree, []yur.Hash{parent}, c
		h, err := yur.WriteRev(r.Store, rev)
		if err != nil {
			return err
		}
		parent, parentTree = h, tree
		for _, p := range c {
			if !contains(conflicts, p) {
				conflicts = append(conflicts, p)
			}
		}
	}
	_, behind := r.aheadBehind(b.Head, remote)
	b.Head = parent
	fmt.Printf("Brought %s from the server; your %s go on top of them\n", plural(behind, "rev"), plural(len(local), "rev"))
	if err := r.carryWork(b, oldHead, oldWork, parentTree); err != nil {
		return err
	}
	if len(conflicts) > 0 {
		b.Conflicts = append(b.Conflicts, conflicts...)
		// Ours is always what was here, theirs what came: the words mean the same in every command. The
		// sides carryWork noted, with the unsaved work in them, are the better ones to keep.
		if b.Merge == nil {
			b.Merge = &Merge{Name: "the server", Base: oldHead, Ours: oldHead, Theirs: serverTree}
		}
		printConflicts(conflicts)
	}
	return nil
}

func removeString(list []string, s string) []string {
	out := []string{}
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// The unsaved work, made against oldHead, laid over a new head
func (r *Repo) carryWork(b *Branch, oldHead, oldWork, newHead yur.Hash) error {
	result, conflicts, err := yur.MergeTrees(r.Store, oldHead, newHead, oldWork, "server", "yours")
	if err != nil {
		return err
	}
	if err := r.checkout(oldWork, result); err != nil {
		return err
	}
	b.Work = yur.Zero
	if err := r.setWork(b, result); err != nil {
		return err
	}
	if len(conflicts) > 0 {
		b.Conflicts = append(b.Conflicts, conflicts...)
		b.Merge = &Merge{Name: "the server", Base: oldHead, Ours: oldWork, Theirs: newHead}
		printConflicts(conflicts)
	}
	return nil
}

func printConflicts(list []string) {
	fmt.Println(paint(red, plural(len(list), "conflict")) + " with the server's changes:")
	for _, p := range list {
		fmt.Println("  " + paint(red, "!") + " " + p)
	}
	fmt.Println(paint(dim, "Settle them and yur save; yur undo takes the whole sync back."))
}

func (r *Repo) push(name string, force bool, allowSecrets bool) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	br, ok := r.State.Branches[name]
	if !ok {
		return errors.New("no branch " + name)
	}
	if br.Head.IsZero() {
		return errors.New("nothing saved on " + name + " yet: yur save first")
	}
	if name == r.State.Branch && len(r.openConflicts(br)) > 0 {
		return errors.New("conflicts are still open: settle them and yur save before pushing")
	}
	remote := rm.Branches[name]
	// Any rev going out, not just the last: markers an earlier one saved are still in the files
	sending, revs, _ := yur.Only(r.Store, []yur.Hash{br.Head}, []yur.Hash{remote}, 0)
	for _, h := range sending {
		if len(revs[h].Conflicts) > 0 {
			return errors.New("rev " + h.Short() + " was saved with conflicts (" + strings.Join(revs[h].Conflicts, ", ") + "): settle them and save again")
		}
	}
	if remote == br.Head {
		fmt.Println(paint(dim, "The server has "+name+" as it is here"))
		return nil
	}
	if !force && !remote.IsZero() && !yur.IsAncestor(r.Store, remote, br.Head) {
		return apiError{Detail: "not_fast_forward"}
	}
	if err := r.sendRevs([]yur.Hash{br.Head}); err != nil {
		return err
	}
	var answer struct{ Web string }
	body := map[string]interface{}{"branch": name, "old": remote, "new": br.Head, "force": force, "allow_secrets": allowSecrets}
	if err := api("POST", projectPath(rm.Project)+"/update", body, &answer); err != nil {
		return err
	}
	// A new branch is counted from the main one, not from nothing
	from := remote
	if from.IsZero() {
		from = rm.Branches[rm.Default]
	}
	ahead, _ := r.aheadBehind(br.Head, from)
	rm.Branches[name] = br.Head
	rm.Gone = removeString(rm.Gone, name)
	fmt.Printf("Sent %s to %s %s\n", plural(ahead, "rev"), paint(cyan, rm.Project+"/"+name), paint(dim, answer.Web))
	return nil
}

func cmdSync(r *Repo, a *args) error {
	if _, err := r.refresh(); err != nil {
		return err
	}
	remote := r.State.Remote.Branches[r.State.Branch]
	if err := r.integrate(remote, a.bool("merge")); err != nil {
		return err
	}
	if a.bool("no-push") {
		return nil
	}
	b := r.State.current()
	if len(r.openConflicts(b)) > 0 || b.Head.IsZero() {
		return nil
	}
	// A merge waiting in the unsaved work is sent once it is saved
	if b.Merge != nil && b.Merge.Parent != nil {
		fmt.Println(paint(dim, "The merge waits in your unsaved work: yur save, then yur sync, sends it."))
		return nil
	}
	if contains(r.State.Remote.Gone, r.State.Branch) {
		fmt.Println(paint(yellow, "This branch was removed on the server; yur push puts it back there."))
		return nil
	}
	if err := r.push(r.State.Branch, false, a.bool("allow-secrets")); err != nil {
		return err
	}
	if !b.Work.IsZero() {
		fmt.Println(paint(dim, "Unsaved changes stay here: yur save, then yur sync, sends them too."))
	}
	return nil
}

func cmdPull(r *Repo, a *args) error {
	if _, err := r.refresh(); err != nil {
		return err
	}
	remote := r.State.Remote.Branches[r.State.Branch]
	if remote == r.State.current().Head || remote.IsZero() {
		fmt.Println(paint(dim, "Nothing new on the server"))
	}
	return r.integrate(remote, a.bool("merge"))
}

func cmdPush(r *Repo, a *args) error {
	if _, err := r.refresh(); err != nil {
		return err
	}
	name := a.arg(0)
	if name == "" {
		name = r.State.Branch
	}
	if err := r.push(name, a.bool("force", "f"), a.bool("allow-secrets")); err != nil {
		return err
	}
	if a.bool("tags") {
		return r.pushTags(a.bool("force", "f"))
	}
	return nil
}

type ProjectModel struct {
	ID          int
	Name        string
	Description string
	Visibility  string
	Owner       struct {
		ID   int
		Link string
		Name string
	}
	Web string
}

func (p ProjectModel) path() string { return p.Owner.Link + "/" + p.Name }

func cmdCreate(r *Repo, a *args) error {
	if config.Token == "" {
		return errors.New("sign in first: yur login")
	}
	if r.State.Remote != nil && r.State.Remote.Project != "" {
		return errors.New("the project goes to " + r.State.Remote.Project + " already")
	}
	name := a.arg(0)
	if name == "" {
		name = filepath.Base(r.Root)
	}
	visibility := "public"
	if a.bool("private") {
		visibility = "private"
	}
	var project ProjectModel
	body := map[string]interface{}{"name": name, "description": a.get("d", "description"), "visibility": visibility, "default_branch": r.State.Branch}
	if err := api("POST", "/projects", body, &project); err != nil {
		return err
	}
	r.State.Remote = &Remote{Project: project.path(), Branches: map[string]yur.Hash{}, Default: r.State.Branch}
	fmt.Printf("Made %s on Yurba %s\n", paint(cyan, project.path()), paint(dim, project.Web))
	if r.State.current().Head.IsZero() {
		fmt.Println(paint(dim, "Save something (yur save -m ...) and yur sync sends it."))
		return nil
	}
	return r.push(r.State.Branch, false, false)
}

// owner/project, or the address of its page
func parseProject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "/projects/"); i >= 0 {
		s = s[i+len("/projects/"):]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("a project is owner/name, e.g. yurba/yur")
	}
	return parts[0] + "/" + parts[1], nil
}

func cmdRemote(r *Repo, a *args) error {
	if a.arg(0) == "" {
		if r.State.Remote == nil || r.State.Remote.Project == "" {
			fmt.Println(paint(dim, "Not linked to a Yurba project"))
			return nil
		}
		fmt.Printf("%s %s\n", r.State.Remote.Project, paint(dim, config.Web+"/projects/"+r.State.Remote.Project))
		return nil
	}
	project, err := parseProject(a.arg(0))
	if err != nil {
		return err
	}
	refs, err := fetchRefs(project)
	if err != nil {
		return err
	}
	r.State.Remote = &Remote{Project: project, Branches: map[string]yur.Hash{}, Default: refs.Default}
	if _, err := r.refresh(); err != nil {
		return err
	}
	fmt.Printf("Linked to %s; yur sync brings its changes\n", paint(cyan, project))
	return nil
}

func cmdClone(_ *Repo, a *args) error {
	project, err := parseProject(a.arg(0))
	if err != nil {
		return err
	}
	refs, err := fetchRefs(project)
	if err != nil {
		return err
	}
	folder := a.arg(1)
	if folder == "" {
		folder = strings.Split(project, "/")[1]
	}
	root, _ := filepath.Abs(folder)
	entries, err := os.ReadDir(root)
	if err == nil && len(entries) > 0 {
		return errors.New(folder + " is not empty")
	}
	// A clone that fails leaves nothing: not the folder it made, nor anything in the empty one it was given
	existed := err == nil
	undo := func() {
		if !existed {
			os.RemoveAll(root)
			return
		}
		left, _ := os.ReadDir(root)
		for _, entry := range left {
			os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
	branch := refs.Default
	if branch == "" {
		branch = "main"
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	r, err := initRepo(root, branch)
	if err != nil {
		return err
	}
	full := a.bool("full")
	r.State.Remote = &Remote{Project: project, Branches: map[string]yur.Hash{}, Default: branch, Light: !full}
	fmt.Printf("Cloning %s into %s\n", paint(cyan, project), folder)
	if _, err := r.refresh(); err != nil {
		undo()
		return err
	}
	head := r.State.Remote.Branches[branch]
	r.State.Branches[branch] = &Branch{Head: head}
	if !head.IsZero() {
		if err := r.checkout(yur.EmptyTree(r.Store), r.revTree(head)); err != nil {
			undo()
			return err
		}
	}
	r.before = r.State.clone()
	if err := r.saveState(); err != nil {
		return err
	}
	fmt.Printf("Done. cd %s, and yur status\n", folder)
	if !full {
		fmt.Println(paint(dim, "Files of old revs come from the server when first needed (--full brings all now)."))
	}
	return nil
}

func cmdPropose(r *Repo, a *args) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	if _, err := r.refresh(); err != nil {
		return err
	}
	target := a.get("to")
	if target == "" {
		target = rm.Default
	}
	if target == r.State.Branch {
		return errors.New("you are on " + target + " itself: make a branch for the change (yur switch -c my-change)")
	}
	if err := r.push(r.State.Branch, false, false); err != nil {
		return err
	}
	title := strings.Join(a.list, " ")
	if title == "" {
		if rev, err := yur.ReadRev(r.Store, r.State.current().Head); err == nil {
			title = rev.Title()
		}
	}
	var proposal struct {
		Number int
		Web    string
	}
	body := map[string]interface{}{"title": title, "body": a.get("m", "message"), "source": r.State.Branch, "target": target, "draft": a.bool("draft")}
	if err := api("POST", "/projects/"+rm.Project+"/proposals", body, &proposal); err != nil {
		return err
	}
	fmt.Printf("Proposal #%d: %s into %s\n%s\n", proposal.Number, paint(cyan, r.State.Branch), paint(cyan, target), proposal.Web)
	return nil
}

func cmdRelease(r *Repo, a *args) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	tag := a.arg(0)
	if tag == "" || !yur.ValidRef(tag) {
		return errors.New("name the release: yur release v1.0.0")
	}
	rev := r.State.current().Head
	if name := a.get("r", "rev"); name != "" {
		if rev, err = r.resolve(name); err != nil {
			return err
		}
	}
	if rev.IsZero() {
		return errors.New("nothing saved to release")
	}
	var release struct {
		Web string
		Cdn string
		Zip string
	}
	body := map[string]interface{}{"tag": tag, "rev": rev, "title": tag, "notes": a.get("m", "message"), "prerelease": a.bool("pre")}
	if err := api("POST", "/projects/"+rm.Project+"/releases", body, &release); err != nil {
		return err
	}
	fmt.Printf("Released %s\n  page  %s\n  zip   %s\n  cdn   %s\n", paint(cyan, tag), release.Web, release.Zip, release.Cdn)
	return nil
}

func cmdOpen(r *Repo, a *args) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	url := config.Web + "/projects/" + rm.Project
	if p := a.arg(0); p != "" {
		paths, err := r.paths([]string{p})
		if err != nil {
			return err
		}
		url += "/tree/" + r.State.Branch + "/" + paths[0]
	}
	fmt.Println(url)
	openBrowser(url)
	return nil
}

// Account

func cmdLogin(_ *Repo, a *args) error {
	host, _ := os.Hostname()
	var start struct {
		Device  string
		Code    string
		Web     string
		Expires int64
	}
	if err := api("POST", "/yur/login", map[string]string{"device": host}, &start); err != nil {
		return err
	}
	fmt.Printf("Confirm the code %s on Yurba:\n  %s\n", paint(bold, start.Code), start.Web)
	openBrowser(start.Web)
	fmt.Println(paint(dim, "Waiting for the confirmation..."))
	for time.Now().Unix() < start.Expires {
		time.Sleep(2 * time.Second)
		var answer struct {
			Token string
			User  User
		}
		err := api("GET", "/yur/login/"+start.Device, nil, &answer)
		if err != nil {
			if e, ok := err.(apiError); ok && e.Detail == "login_pending" {
				continue
			}
			return err
		}
		config.Token = answer.Token
		fromFile.Token = answer.Token
		config.User = &answer.User
		fromFile.User = config.User
		if err := saveConfig(); err != nil {
			return err
		}
		fmt.Printf("Signed in as %s (@%s)\n", paint(cyan, answer.User.Name), answer.User.Link)
		return nil
	}
	return apiError{Detail: "login_expired"}
}

func cmdLogout(_ *Repo, a *args) error {
	if config.Token != "" {
		api("POST", "/logout", nil, nil)
	}
	config.Token, config.User = "", nil
	fromFile.Token, fromFile.User = "", nil
	if err := saveConfig(); err != nil {
		return err
	}
	fmt.Println("Signed out")
	return nil
}

func cmdWhoami(_ *Repo, a *args) error {
	if !signedIn() {
		fmt.Println(paint(dim, "Not signed in (yur login)"))
		return nil
	}
	fmt.Printf("%s (@%s) on %s\n", config.User.Name, config.User.Link, config.Api)
	if info, err := latest(10 * time.Second); err == nil && info.Storage != nil && info.Storage.Limit > 0 {
		fmt.Printf("Storage: %.1f of %.1f GB used\n", float64(info.Storage.Used)/(1<<30), float64(info.Storage.Limit)/(1<<30))
	}
	return nil
}

func cmdConfig(_ *Repo, a *args) error {
	switch a.arg(0) {
	case "":
		fmt.Printf("api  %s\nweb  %s\n", config.Api, config.Web)
		return nil
	case "api":
		config.Api = strings.TrimRight(a.arg(1), "/")
		fromFile.Api = config.Api
	case "web":
		config.Web = strings.TrimRight(a.arg(1), "/")
		fromFile.Web = config.Web
	default:
		return errors.New("yur config api <url> or yur config web <url>")
	}
	return saveConfig()
}
