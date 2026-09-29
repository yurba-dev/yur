package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"yurba.one/yur"
)

// Checks and events: what tools say of a rev, and a way for them to follow a project

type checkModel struct {
	Name        string
	State       string
	Description string
	Url         string
	User        *struct{ Name, Link string }
	Updated     int64
}

var checkMarks = map[string][2]string{
	"success": {"✓", green},
	"failure": {"✗", red},
	"error":   {"!", red},
	"pending": {"●", yellow},
}

// The rev a check is about: the one given, or the saved head of the branch
func (r *Repo) checkedRev(a *args) (yur.Hash, error) {
	name := a.get("r", "rev")
	if name == "" {
		name = r.State.Branch
	}
	h, err := r.resolve(name)
	if err != nil {
		return yur.Zero, err
	}
	if h == r.State.current().Work {
		h = r.State.current().Head
	}
	if h.IsZero() {
		return yur.Zero, errors.New("nothing saved yet")
	}
	return h, nil
}

func cmdChecks(r *Repo, a *args) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	if a.arg(0) != "" {
		a.flags["r"] = a.arg(0)
	}
	h, err := r.checkedRev(a)
	if err != nil {
		return err
	}
	var list []checkModel
	if err := api("GET", "/projects/"+rm.Project+"/checks?ref="+h.String(), nil, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println(paint(dim, "No checks for "+h.Short()+" yet"))
		return nil
	}
	for _, c := range list {
		mark := checkMarks[c.State]
		line := fmt.Sprintf("  %s %s", paint(mark[1], mark[0]), paint(bold, c.Name))
		if c.Description != "" {
			line += "  " + c.Description
		}
		line += paint(dim, "  "+ago(c.Updated))
		fmt.Println(line)
		if c.Url != "" {
			fmt.Println("    " + paint(dim, c.Url))
		}
	}
	return nil
}

func cmdCheck(r *Repo, a *args) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	name, state := a.arg(0), a.arg(1)
	if name == "" || state == "" {
		return errors.New("yur check <name> <pending|success|failure|error> [-m description] [--url address] [-r rev]")
	}
	if _, ok := checkMarks[state]; !ok {
		return errors.New("a check is pending, success, failure or error")
	}
	h, err := r.checkedRev(a)
	if err != nil {
		return err
	}
	body := map[string]interface{}{"rev": h, "name": name, "state": state, "description": a.get("m", "message"), "url": a.get("url")}
	if err := api("POST", "/projects/"+rm.Project+"/checks", body, nil); err != nil {
		if e, ok := err.(apiError); ok && e.Detail == "rev_not_found" {
			return errors.New("the server doesn't have " + h.Short() + " yet: push it first")
		}
		return err
	}
	mark := checkMarks[state]
	fmt.Printf("%s %s on %s\n", paint(mark[1], mark[0]), name, paint(dim, h.Short()))
	return nil
}

type eventModel struct {
	ID        int
	Type      string
	User      *struct{ Name, Link string }
	Data      map[string]interface{}
	Timestamp int64
}

// Follows a project: waits on the server for what happens and prints it, line by line.
// Out of a project folder the project is named: yur watch owner/project.
func cmdWatch(_ *Repo, a *args) error {
	project := ""
	if a.arg(0) != "" {
		p, err := parseProject(a.arg(0))
		if err != nil {
			return err
		}
		project = p
	} else {
		root, err := findRoot(".")
		if err != nil {
			return errors.New("which project? yur watch owner/project, or run it in a project folder")
		}
		r, err := loadRepo(root)
		if err != nil {
			return err
		}
		rm, err := r.needRemote()
		if err != nil {
			return err
		}
		project = rm.Project
	}
	asJSON := a.bool("json")
	after, _ := strconv.Atoi(a.get("after"))
	if after <= 0 {
		var start struct{ Last int }
		if err := api("GET", "/projects/"+project+"/events?after=0", nil, &start); err != nil {
			return err
		}
		after = start.Last
	}
	if !asJSON {
		fmt.Fprintln(os.Stderr, paint(dim, "Following "+project+" (Ctrl+C stops)"))
	}
	for {
		var got struct {
			Events []eventModel
			Last   int
		}
		err := api("GET", "/projects/"+project+"/events?wait=50&after="+strconv.Itoa(after), nil, &got)
		if err != nil {
			if e, ok := err.(apiError); ok && e.Status >= 400 && e.Status < 500 && e.Status != 429 {
				return err
			}
			// The network or the server let it drop: try again in a moment
			time.Sleep(5 * time.Second)
			continue
		}
		for _, ev := range got.Events {
			if asJSON {
				line, _ := json.Marshal(ev)
				fmt.Println(string(line))
			} else {
				fmt.Println(describeEvent(ev))
			}
		}
		if got.Last > after {
			after = got.Last
		}
	}
}

func describeEvent(ev eventModel) string {
	who := "someone"
	if ev.User != nil {
		who = ev.User.Name
	}
	d := ev.Data
	text := func(key string) string {
		if v, ok := d[key]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}
	what := strings.ReplaceAll(ev.Type, "_", " ")
	switch ev.Type {
	case "pushed":
		what = "pushed " + text("count") + " rev(s) to " + paint(cyan, text("branch"))
	case "tagged":
		what = "tagged " + paint(cyan, text("tag"))
	case "released":
		what = "released " + paint(cyan, text("tag"))
	case "checked":
		mark := checkMarks[text("state")]
		what = paint(mark[1], mark[0]) + " " + text("name") + " on " + shortHash(text("rev"))
	case "branch_removed":
		what = "removed the branch " + text("branch")
	}
	if n := text("number"); n != "" {
		what += " #" + n + " " + text("title")
	}
	return fmt.Sprintf("%s %s %s", paint(dim, time.Unix(ev.Timestamp, 0).Format("15:04:05")), who, what)
}

func shortHash(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
