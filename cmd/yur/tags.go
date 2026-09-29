package main

import (
	"errors"
	"fmt"
	"sort"

	"yurba.one/yur"
)

// Tags: a name kept on a rev. They are made here and sent with yur push --tags.

func cmdTag(r *Repo, a *args) error {
	if name := a.get("d", "delete"); name != "" {
		if a.bool("server") {
			rm, err := r.needRemote()
			if err != nil {
				return err
			}
			if err := api("POST", projectPath(rm.Project)+"/tag", map[string]interface{}{"name": name, "delete": true}, nil); err != nil {
				return err
			}
			delete(rm.Tags, name)
			fmt.Printf("Removed the tag %s from the server\n", name)
			return nil
		}
		if _, ok := r.State.Tags[name]; !ok {
			return errors.New("no tag " + name + " here (--server removes one from the server)")
		}
		delete(r.State.Tags, name)
		fmt.Printf("Removed the tag %s %s\n", name, paint(dim, "(yur undo brings it back)"))
		return nil
	}
	name := a.arg(0)
	if name == "" {
		return listTags(r)
	}
	if !yur.ValidRef(name) {
		return errors.New("a tag name is letters, digits and - _ . / + only")
	}
	if _, ok := r.State.Tags[name]; ok && !a.bool("force", "f") {
		return errors.New("there is a tag " + name + " already (--force moves it)")
	}
	h, err := r.resolve(a.get("r", "rev"))
	if err != nil {
		return err
	}
	// The saved rev under the work: a tag names something saved
	if b := r.State.current(); h == b.Work {
		h = b.Head
	}
	if h.IsZero() {
		return errors.New("nothing saved to tag yet")
	}
	if r.State.Tags == nil {
		r.State.Tags = map[string]yur.Hash{}
	}
	r.State.Tags[name] = h
	rev, _ := yur.ReadRev(r.Store, h)
	fmt.Printf("Tagged %s %s as %s %s\n", paint(dim, h.Short()), rev.Title(), paint(cyan, name), paint(dim, "(yur push --tags sends it)"))
	return nil
}

func listTags(r *Repo) error {
	names := map[string]bool{}
	for n := range r.State.Tags {
		names[n] = true
	}
	var server map[string]yur.Hash
	if r.State.Remote != nil {
		server = r.State.Remote.Tags
		for n := range server {
			names[n] = true
		}
	}
	if len(names) == 0 {
		fmt.Println(paint(dim, "No tags: yur tag <name> puts one on the current rev"))
		return nil
	}
	list := []string{}
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	for _, n := range list {
		local, here := r.State.Tags[n]
		remote, there := server[n]
		h := local
		if !here {
			h = remote
		}
		title := ""
		if rev, err := yur.ReadRev(r.Store, h); err == nil {
			title = rev.Title()
		}
		mark := ""
		switch {
		case here && !there:
			mark = paint(yellow, " (not sent)")
		case !here:
			mark = paint(blue, " (server)")
		case local != remote:
			mark = paint(red, " (differs from the server's)")
		}
		fmt.Printf("  %s  %s %s%s\n", paint(cyan, n), paint(dim, h.Short()), title, mark)
	}
	return nil
}

// Sends the tags the server hasn't got, and moves those it has elsewhere with --force
func (r *Repo) pushTags(force bool) error {
	rm, err := r.needRemote()
	if err != nil {
		return err
	}
	sent := 0
	names := []string{}
	for n := range r.State.Tags {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		h := r.State.Tags[name]
		if rm.Tags[name] == h {
			continue
		}
		if _, there := rm.Tags[name]; there && !force {
			fmt.Printf("%s %s\n", paint(yellow, "Skipped "+name+":"), "the server has it on another rev (--force moves it)")
			continue
		}
		if err := r.sendRevs([]yur.Hash{h}); err != nil {
			return err
		}
		if err := api("POST", projectPath(rm.Project)+"/tag", map[string]interface{}{"name": name, "rev": h, "force": force}, nil); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if rm.Tags == nil {
			rm.Tags = map[string]yur.Hash{}
		}
		rm.Tags[name] = h
		sent++
	}
	if sent == 0 {
		fmt.Println(paint(dim, "The server has every tag already"))
		return nil
	}
	fmt.Printf("Sent %s\n", plural(sent, "tag"))
	return nil
}
