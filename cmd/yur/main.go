// yur, the command line of Yurba Projects.
//
//	go build -o yur ./cmd/yur
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"yurba.one/yur"
)

var version = yur.Version

type command struct {
	name    string
	aliases []string
	args    string
	about   string
	// The folder is read into the branch before the command runs and the state saved after it
	repo bool
	run  func(r *Repo, a *args) error
}

var commands []command

func init() {
	commands = []command{
		{name: "init", args: "[folder] [--branch name]", about: "Start a project in a folder", run: cmdInit},
		{name: "clone", args: "<owner/project> [folder] [--full]", about: "Bring a project from Yurba (old files come when first needed)", run: cmdClone},
		{name: "status", aliases: []string{"st"}, about: "What changed and where you are", repo: true, run: cmdStatus},
		{name: "diff", args: "[paths] [-r rev | rev..rev] [--stat]", about: "The changes line by line", repo: true, run: cmdDiff},
		{name: "save", aliases: []string{"commit"}, args: "-m <message> [paths]", about: "Save the changes (all, or of some paths) as a rev", repo: true, run: cmdSave},
		{name: "amend", args: "[-m message] [paths]", about: "Put the changes into the last rev instead of a new one", repo: true, run: cmdAmend},
		{name: "log", args: "[path] [-n count] [--all]", about: "History", repo: true, run: cmdLog},
		{name: "show", args: "[rev]", about: "One rev and its changes", repo: true, run: cmdShow},
		{name: "undo", about: "Take back the last command, whatever it was", repo: true, run: cmdUndo},
		{name: "redo", about: "Do again what undo took back", repo: true, run: cmdRedo},
		{name: "ops", args: "[-n count]", about: "The commands undo can take back", repo: true, run: cmdOps},
		{name: "restore", args: "[paths] [--from rev]", about: "Put files back as they were (all of them without paths)", repo: true, run: cmdRestore},
		{name: "revert", args: "<rev>", about: "Take back the changes of one rev, as a new rev", repo: true, run: cmdRevert},
		{name: "pick", aliases: []string{"cherry-pick"}, args: "<rev>", about: "Bring the changes of one rev from elsewhere onto this branch", repo: true, run: cmdPick},
		{name: "blame", args: "<path> [-r rev]", about: "Which rev brought in each line of a file", repo: true, run: cmdBlame},
		{name: "grep", args: "<pattern> [paths] [-i] [-l] [-r rev]", about: "Lines that match, in the files of the work or of a rev", repo: true, run: cmdGrep},
		{name: "tag", args: "[name [-r rev] [--force]] [-d name [--server]]", about: "List, put or remove tags (yur push --tags sends them)", repo: true, run: cmdTag},
		{name: "branch", args: "[name [--from rev]] [-d name] [-m old new]", about: "List, make, rename or remove branches", repo: true, run: cmdBranch},
		{name: "switch", aliases: []string{"sw"}, args: "<branch> [-c]", about: "Go to a branch; unsaved work stays with its own branch (-c makes one and takes it along)", repo: true, run: cmdSwitch},
		{name: "merge", args: "<branch> [--abort]", about: "Bring another branch's changes in", repo: true, run: cmdMerge},
		{name: "resolve", args: "[paths] [--ours | --theirs]", about: "Show conflicts, or settle them: --ours keeps yours, --theirs takes what came", repo: true, run: cmdResolve},
		{name: "sync", args: "[--merge] [--no-push]", about: "Take the server's changes and give yours, in one step", repo: true, run: cmdSync},
		{name: "pull", about: "Take the server's changes", repo: true, run: cmdPull},
		{name: "push", args: "[branch] [--force] [--tags]", about: "Give your saved changes to the server (--tags: the tags too)", repo: true, run: cmdPush},
		{name: "create", args: "[name] [--private] [-d description]", about: "Make this project on Yurba and send it there", repo: true, run: cmdCreate},
		{name: "remote", args: "[owner/project]", about: "Show or set the Yurba project this one goes to", repo: true, run: cmdRemote},
		{name: "propose", args: "[title] [--to branch] [--draft]", about: "Push the branch and propose it into another one", repo: true, run: cmdPropose},
		{name: "release", args: "<tag> [-m notes] [--rev rev] [--pre]", about: "Publish a version: a download and a CDN address", repo: true, run: cmdRelease},
		{name: "open", args: "[path]", about: "Open the project on Yurba", repo: true, run: cmdOpen},
		{name: "checks", args: "[rev]", about: "What builds and tests said of a rev", repo: true, run: cmdChecks},
		{name: "check", args: "<name> <pending|success|failure|error> [-m text] [--url address] [-r rev]", about: "Report a check of a rev (from a build or a test run)", repo: true, run: cmdCheck},
		{name: "watch", args: "[owner/project] [--json] [--after id]", about: "Follow what happens in the project, as it happens", run: cmdWatch},
		{name: "ignore", args: "<pattern>", about: "Leave files out (.yurignore)", repo: true, run: cmdIgnore},
		{name: "import-git", args: "[folder]", about: "Bring the whole history of a git repository in", run: cmdImportGit},
		{name: "export-git", args: "[folder]", about: "Write the history into a git repository (again: only what is new)", repo: true, run: cmdExportGit},
		{name: "gc", about: "Drop objects nothing needs any more", repo: true, run: cmdGc},
		{name: "login", about: "Sign in with your Yurba account in the browser", run: cmdLogin},
		{name: "logout", about: "Sign out on this computer", run: cmdLogout},
		{name: "whoami", about: "Who you are signed in as", run: cmdWhoami},
		{name: "config", args: "[api|web <url>]", about: "Show or set where Yurba is", run: cmdConfig},
		{name: "version", about: "The version of yur", run: func(r *Repo, a *args) error { fmt.Println("yur " + version); return nil }},
		{name: "update", args: "[--yes]", about: "Get the newest yur (asks before downloading)", run: cmdUpdate},
		{name: "completion", args: "<bash|zsh|powershell>", about: "Tab completion of commands, branches and tags for a shell", run: cmdCompletion},
		{name: "help", args: "[command]", about: "This list, or one command", run: cmdHelp},
	}
}

func find(name string) *command {
	for i, c := range commands {
		if c.name == name {
			return &commands[i]
		}
		for _, alias := range c.aliases {
			if alias == name {
				return &commands[i]
			}
		}
	}
	return nil
}

func main() {
	setupTerminal()
	dropOldBuild()
	if len(os.Args) >= 2 && os.Args[1] == "__complete" {
		complete(os.Args[2:])
		return
	}
	if len(os.Args) < 2 {
		cmdHelp(nil, &args{})
		return
	}
	c := find(os.Args[1])
	if c == nil {
		fail(fmt.Errorf("no command %q (yur help lists them)", os.Args[1]))
	}
	a := parseArgs(os.Args[2:])
	if a.bool("help", "h") {
		cmdHelp(nil, &args{list: []string{c.name}})
		return
	}
	if !c.repo {
		if err := c.run(nil, a); err != nil {
			fail(err)
		}
		if c.name != "update" && c.name != "completion" {
			tellUpdate()
		}
		return
	}
	r, err := openRepo()
	if err != nil {
		fail(err)
	}
	if err := r.acquire(); err != nil {
		fail(err)
	}
	watchInterrupt(r.release)
	err = runInRepo(r, c, a)
	r.release()
	if err != nil {
		fail(err)
	}
	tellUpdate()
	restoreTerminal()
}

func runInRepo(r *Repo, c *command, a *args) error {
	if err := r.snapshot(); err != nil {
		return err
	}
	// The snapshot is an operation of its own, so undo takes back a command and never the typing before it
	if err := r.saveState(); err != nil {
		return err
	}
	r.record("snapshot", 0)
	// A command that fails leaves the state as it found it: half a sync (a new head, the old files) would
	// look like the others' changes undone. Files it had already written show up as unsaved work.
	start := r.State.clone()
	if err := c.run(r, a); err != nil {
		r.State = start
		r.saveState()
		return err
	}
	if err := r.saveState(); err != nil {
		return err
	}
	if c.name != "undo" && c.name != "redo" {
		return r.record(strings.Join(os.Args[1:], " "), 0)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, paint(red, "error: ")+err.Error())
	restoreTerminal()
	os.Exit(1)
}

// Arguments: flags anywhere, -m value and --name=value both, the rest in order
type args struct {
	list  []string
	flags map[string]string
}

var valueFlags = map[string]bool{"b": true, "m": true, "message": true, "r": true, "rev": true, "from": true, "to": true, "n": true, "branch": true, "d": true, "description": true, "delete": true, "url": true, "after": true}

func parseArgs(raw []string) *args {
	a := &args{flags: map[string]string{}}
	for i := 0; i < len(raw); i++ {
		arg := raw[i]
		if arg == "--" {
			a.list = append(a.list, raw[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			a.list = append(a.list, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if key, value, ok := strings.Cut(name, "="); ok {
			a.flags[key] = value
			continue
		}
		if valueFlags[name] && i+1 < len(raw) {
			a.flags[name] = raw[i+1]
			i++
			continue
		}
		a.flags[name] = "true"
	}
	return a
}

func (a *args) get(names ...string) string {
	for _, n := range names {
		if v, ok := a.flags[n]; ok {
			return v
		}
	}
	return ""
}

// --flag=false and --flag=0 mean off
func (a *args) bool(names ...string) bool {
	v := a.get(names...)
	return v != "" && v != "false" && v != "0"
}

func (a *args) arg(i int) string {
	if i < len(a.list) {
		return a.list[i]
	}
	return ""
}

// Output

var isTerminal = func() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}()

var colors = isTerminal && os.Getenv("NO_COLOR") == ""

const (
	red    = "31"
	green  = "32"
	yellow = "33"
	blue   = "34"
	purple = "35"
	cyan   = "36"
	dim    = "2"
	bold   = "1"
)

func paint(color, text string) string {
	if !colors {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

func ago(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	}
	return time.Unix(unix, 0).Format("2 Jan 2006")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func printSecrets(list []yur.Secret) {
	fmt.Fprintln(os.Stderr, paint(red, "Keys or passwords found; they must not be published:"))
	for _, s := range list {
		where := s.Path
		if s.Line > 0 {
			where = fmt.Sprintf("%s:%d", s.Path, s.Line)
		}
		fmt.Fprintf(os.Stderr, "  %s  %s\n", paint(yellow, where), s.Kind)
	}
	fmt.Fprintln(os.Stderr, paint(dim, "Take them out (an ignored file, an environment variable) and save again; --allow-secrets keeps them anyway."))
}

func cmdHelp(r *Repo, a *args) error {
	if name := a.arg(0); name != "" {
		c := find(name)
		if c == nil {
			return fmt.Errorf("no command %q", name)
		}
		fmt.Printf("%s %s\n\n  %s\n", paint(bold, "yur "+c.name), c.args, c.about)
		if len(c.aliases) > 0 {
			fmt.Printf("\n  Also: %s\n", strings.Join(c.aliases, ", "))
		}
		return nil
	}
	fmt.Print(paint(bold, "yur") + " - version control of Yurba Projects\n\n")
	fmt.Print("No staging, nothing to stash: whatever you type is kept, and every command can be undone.\n\n")
	groups := []struct {
		title string
		names []string
	}{
		{"Start", []string{"init", "clone", "import-git", "create"}},
		{"Work", []string{"status", "diff", "save", "amend", "restore", "revert", "pick", "undo", "redo", "ops"}},
		{"History", []string{"log", "show", "blame", "grep", "tag"}},
		{"Branches", []string{"branch", "switch", "merge", "resolve"}},
		{"Yurba", []string{"sync", "pull", "push", "propose", "release", "open", "remote"}},
		{"Tools", []string{"checks", "check", "watch", "export-git"}},
		{"Account", []string{"login", "logout", "whoami", "config"}},
		{"Other", []string{"ignore", "gc", "completion", "update", "version", "help"}},
	}
	width := 0
	for _, c := range commands {
		if len(c.name) > width {
			width = len(c.name)
		}
	}
	for _, g := range groups {
		fmt.Println(paint(dim, g.title))
		sort.Strings(g.names)
		for _, name := range g.names {
			c := find(name)
			fmt.Printf("  %s  %s\n", paint(cyan, fmt.Sprintf("%-*s", width, c.name)), c.about)
		}
	}
	fmt.Println("\nyur help <command> tells more of one.")
	return nil
}
