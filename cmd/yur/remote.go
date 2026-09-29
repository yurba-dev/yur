package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"yurba.one/yur"
)

// Config

// The account signed in, as the config keeps it and the server gives it
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Link string `json:"link"`
}

type Config struct {
	Api   string `json:"api"`
	Web   string `json:"web"`
	Token string `json:"token,omitempty"`
	User  *User  `json:"user,omitempty"`
	// When the newest version was last asked, and what it was
	Checked int64  `json:"checked,omitempty"`
	Latest  string `json:"latest,omitempty"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".yur", "config.json")
}

var config = loadConfig()

// The file as it was read: what the environment sets for one run is never written into it
var fromFile Config

func loadConfig() *Config {
	c := &Config{Api: "https://api.yurba.one", Web: "https://yurba.one"}
	if data, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(data, c)
	}
	fromFile = *c
	if v := os.Getenv("YUR_API"); v != "" {
		c.Api = v
	}
	if v := os.Getenv("YUR_WEB"); v != "" {
		c.Web = v
	}
	if v := os.Getenv("YUR_TOKEN"); v != "" {
		c.Token = v
	}
	c.Api = strings.TrimRight(c.Api, "/")
	c.Web = strings.TrimRight(c.Web, "/")
	return c
}

// The token is the account's key, so only its owner may read the file
func saveConfig() error {
	os.MkdirAll(filepath.Dir(configPath()), 0700)
	out := *config
	if os.Getenv("YUR_API") != "" {
		out.Api = fromFile.Api
	}
	if os.Getenv("YUR_WEB") != "" {
		out.Web = fromFile.Web
	}
	if os.Getenv("YUR_TOKEN") != "" {
		out.Token, out.User = fromFile.Token, fromFile.User
	}
	data, _ := json.MarshalIndent(out, "", "\t")
	return os.WriteFile(configPath(), data, 0600)
}

// A token from YUR_TOKEN comes without the account it belongs to, so the server is asked once
func signedIn() bool {
	if config.Token == "" {
		return false
	}
	if config.User == nil {
		var u User
		if api("GET", "/yur/me", nil, &u) != nil || u.ID == 0 {
			return false
		}
		config.User = &u
	}
	return true
}

func me() yur.Author {
	if signedIn() {
		return yur.Author{ID: config.User.ID, Name: config.User.Name}
	}
	name := "someone"
	if u, err := user.Current(); err == nil {
		name = u.Username
		if u.Name != "" {
			name = u.Name
		}
	}
	return yur.Author{Name: name}
}

// HTTP

var client = &http.Client{Timeout: 30 * time.Minute}

type apiError struct {
	Status int
	Detail string
}

var errorTexts = map[string]string{
	"token_not_found":    "you are not signed in, or the sign-in has expired (yur login)",
	"project_not_found":  "no such project, or it is private and not yours",
	"access_denied":      "you may not do this in the project",
	"not_fast_forward":   "the branch has moved on the server; yur sync brings its changes first",
	"branch_protected":   "the branch is protected: changes go in through a proposal (yur propose)",
	"ref_moved":          "the branch changed on the server while you pushed; run it again",
	"secrets_found":      "the changes hold keys or passwords",
	"storage_full":       "the storage of the project's owner is full: delete something or take a bigger plan",
	"tag_exists":         "the server has this tag on another rev (--force moves it, for maintainers)",
	"tag_not_found":      "no such tag on the server",
	"bad_tag":            "a tag name is letters, digits and - _ . / + only",
	"bad_check":          "a check is a name of letters, digits and - _ . / : or spaces, with a web address if any",
	"checks_pending":     "the checks the branch asks for haven't passed yet",
	"checks_failed":      "a check the branch asks for has failed",
	"objects_missing":    "the server lacks part of the changes; run it again",
	"too_many_attempts":  "too many requests, wait a little",
	"project_exists":     "you already have a project with this name",
	"bad_name":           "a project name is letters, digits, - _ and . only",
	"login_expired":      "the sign-in took too long, start again",
	"release_exists":     "there is a release with this name already",
	"rev_not_found":      "the server doesn't have this rev: push it first",
	"branch_not_found":   "no such branch on the server",
	"proposal_exists":    "this branch already has an open proposal",
	"nothing_to_propose": "the branch has nothing its target lacks",
}

func (e apiError) Error() string {
	if text, ok := errorTexts[e.Detail]; ok {
		return text
	}
	if e.Detail == "" {
		return fmt.Sprintf("the server answered %d", e.Status)
	}
	return e.Detail
}

func request(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, config.Api+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if config.Token != "" {
		req.Header.Set("token", config.Token)
	}
	req.Header.Set("User-Agent", "yur/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("can't reach Yurba (" + config.Api + "): " + err.Error())
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var fail struct {
			Detail  string
			Secrets []yur.Secret
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		json.Unmarshal(data, &fail)
		if len(fail.Secrets) > 0 {
			printSecrets(fail.Secrets)
		}
		return nil, apiError{Status: resp.StatusCode, Detail: fail.Detail}
	}
	return resp, nil
}

func api(method, path string, in interface{}, out interface{}) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, _ := json.Marshal(in)
		body = bytes.NewReader(data)
		contentType = "application/json"
	}
	resp, err := request(method, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func projectPath(project string) string {
	return "/yur/" + project
}

type RefsModel struct {
	Branches map[string]yur.Hash
	Tags     map[string]yur.Hash
	Default  string
}

func fetchRefs(project string) (RefsModel, error) {
	var refs RefsModel
	err := api("GET", projectPath(project)+"/refs", nil, &refs)
	return refs, err
}

// Brings objects into the store: revs of want not below have, or exactly the objects listed
func (r *Repo) fetch(want, have []yur.Hash, blobs bool, objects []yur.Hash) (int, error) {
	body := map[string]interface{}{"want": want, "have": have, "blobs": blobs, "objects": objects}
	data, _ := json.Marshal(body)
	resp, err := request("POST", projectPath(r.State.Remote.Project)+"/fetch", bytes.NewReader(data), "application/json")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	progress := newProgress("Receiving")
	defer progress.done()
	return yur.ReadPack(resp.Body, r.local(), func(yur.Hash) { progress.add() })
}

// The most hashes one request may list: the server refuses more
const maxListed = 5000

func inParts(hashes []yur.Hash, fn func(part []yur.Hash) error) error {
	for len(hashes) > 0 {
		part := hashes
		if len(part) > maxListed {
			part = part[:maxListed]
		}
		hashes = hashes[len(part):]
		if err := fn(part); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) fetchObjects(hashes []yur.Hash) error {
	if r.State.Remote == nil {
		return errors.New("objects are missing from the project and there is no server to get them from")
	}
	return inParts(hashes, func(part []yur.Hash) error {
		_, err := r.fetch(nil, nil, true, part)
		return err
	})
}

// Sends what the server needs to have heads: the objects below them it lacks, counted from everything
// of the project it has already
func (r *Repo) sendRevs(heads []yur.Hash) error {
	rm := r.State.Remote
	have := []yur.Hash{}
	for _, list := range []map[string]yur.Hash{rm.Branches, rm.Tags} {
		for _, h := range list {
			if r.local().Has(h) {
				have = append(have, h)
			}
		}
	}
	needed, err := yur.Needed(r.Store, heads, have, true)
	if err != nil {
		return err
	}
	return r.upload(needed)
}

func (r *Repo) upload(hashes []yur.Hash) error {
	// Only what the server lacks goes: it may have them from a fork or an earlier try
	missing := []yur.Hash{}
	err := inParts(hashes, func(part []yur.Hash) error {
		var answer struct{ Missing []yur.Hash }
		if err := api("POST", projectPath(r.State.Remote.Project)+"/missing", map[string]interface{}{"hashes": part}, &answer); err != nil {
			return err
		}
		missing = append(missing, answer.Missing...)
		return nil
	})
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	fmt.Printf("Sending %d objects\n", len(missing))
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(yur.WritePack(pw, r.local(), missing))
	}()
	resp, err := request("POST", projectPath(r.State.Remote.Project)+"/upload", pr, "application/octet-stream")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// The store of a light clone: what isn't here yet is asked of the server as it is needed
type lazyStore struct {
	local yur.FileStore
	repo  *Repo
}

func (s *lazyStore) Has(h yur.Hash) bool { return s.local.Has(h) }

func (s *lazyStore) ReadRaw(h yur.Hash) ([]byte, error) {
	if data, err := s.local.ReadRaw(h); err == nil {
		return data, nil
	}
	if s.repo.State.Remote == nil || (config.Token == "" && s.repo.State.Remote.Project == "") {
		return nil, errors.New("object_missing")
	}
	if err := s.repo.fetchObjects([]yur.Hash{h}); err != nil {
		return nil, err
	}
	return s.local.ReadRaw(h)
}

func (s *lazyStore) WriteRaw(h yur.Hash, compressed []byte) error {
	return s.local.WriteRaw(h, compressed)
}

type progress struct {
	label string
	count int
	last  time.Time
	shown bool
}

func newProgress(label string) *progress { return &progress{label: label, last: time.Now()} }

func (p *progress) add() {
	p.count++
	if time.Since(p.last) > 200*time.Millisecond && isTerminal {
		p.last = time.Now()
		p.shown = true
		fmt.Printf("\r%s %d objects", p.label, p.count)
	}
}

func (p *progress) done() {
	if p.shown {
		fmt.Printf("\r%s %d objects, done\n", p.label, p.count)
	}
}
