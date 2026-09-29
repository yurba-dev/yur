package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Updates: yur asks once a day what the newest version is and says so. It downloads only when told to,
// with yur update, and only after the question is answered yes.

type versionInfo struct {
	Version string
	Storage *struct {
		Used  int64
		Limit int64
	}
}

func latest(timeout time.Duration) (versionInfo, error) {
	var info versionInfo
	c := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", config.Api+"/yur/version", nil)
	if err != nil {
		return info, err
	}
	if config.Token != "" {
		req.Header.Set("token", config.Token)
	}
	req.Header.Set("User-Agent", "yur/"+version)
	resp, err := c.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return info, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	err = jsonDecode(resp.Body, &info)
	return info, err
}

func jsonDecode(r io.Reader, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}

// Whether a is later than b, as versions of numbers and dots
func newer(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p, _ = strconv.Atoi(x[i])
		}
		if i < len(y) {
			q, _ = strconv.Atoi(y[i])
		}
		if p != q {
			return p > q
		}
	}
	return false
}

// After a command: once a day, and only at a terminal, where a person reads it
func tellUpdate() {
	if !isTerminal || os.Getenv("YUR_NO_UPDATE_CHECK") != "" {
		return
	}
	if time.Since(time.Unix(config.Checked, 0)) > 24*time.Hour {
		if info, err := latest(2 * time.Second); err == nil {
			config.Latest = info.Version
		}
		config.Checked = time.Now().Unix()
		saveConfig()
	}
	if config.Latest != "" && newer(config.Latest, version) {
		fmt.Fprintln(os.Stderr, paint(dim, "yur "+config.Latest+" is out (this is "+version+"): yur update gets it"))
	}
}

func buildName() (string, error) {
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "windows-amd64":
		return "yur-windows-amd64.exe", nil
	case "linux-amd64", "darwin-amd64", "darwin-arm64":
		return "yur-" + runtime.GOOS + "-" + runtime.GOARCH, nil
	}
	return "", errors.New("there is no ready yur for " + runtime.GOOS + "/" + runtime.GOARCH + ": build it from the source (go build ./cmd/yur)")
}

func cmdUpdate(_ *Repo, a *args) error {
	info, err := latest(15 * time.Second)
	if err != nil {
		return errors.New("can't ask Yurba for the newest yur: " + err.Error())
	}
	config.Latest, config.Checked = info.Version, time.Now().Unix()
	saveConfig()
	if !newer(info.Version, version) {
		fmt.Printf("yur %s is the newest\n", version)
		return nil
	}
	name, err := buildName()
	if err != nil {
		return err
	}
	url := config.Web + "/static/yur/" + name
	size := ""
	short := &http.Client{Timeout: 15 * time.Second}
	if resp, err := short.Head(url); err == nil {
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("the download isn't there (%d): %s", resp.StatusCode, url)
		}
		if resp.ContentLength > 0 {
			size = fmt.Sprintf(", %.1f MB", float64(resp.ContentLength)/(1<<20))
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	fmt.Printf("yur %s is out (this is %s).\nDownload %s%s from %s\nand put it in place of %s?\n", info.Version, version, name, size, url, self)
	if !a.bool("yes", "y") {
		if !isTerminal {
			return errors.New("nothing downloaded: run it at a terminal to answer, or with --yes")
		}
		if answer := strings.ToLower(ask("[y/N] ")); answer != "y" && answer != "yes" {
			fmt.Println("Nothing downloaded")
			return nil
		}
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("the download failed: %d", resp.StatusCode)
	}
	fresh := self + ".new"
	out, err := os.OpenFile(fresh, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return errors.New("can't write next to " + self + ": " + err.Error())
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(fresh)
		return err
	}
	out.Close()
	// A page in place of the program (an error page, a wrong address) must never replace yur
	if err := checkProgram(fresh); err != nil {
		os.Remove(fresh)
		return err
	}
	// Windows keeps a running program's file open, but lets it be renamed away
	if runtime.GOOS == "windows" {
		os.Remove(self + ".old")
		if err := os.Rename(self, self+".old"); err != nil {
			os.Remove(fresh)
			return err
		}
	}
	if err := os.Rename(fresh, self); err != nil {
		if runtime.GOOS == "windows" {
			os.Rename(self+".old", self)
		}
		os.Remove(fresh)
		return err
	}
	fmt.Printf("Updated to yur %s\n", info.Version)
	return nil
}

// The first bytes of a program for this system: MZ on Windows, ELF on Linux, Mach-O on macOS
func checkProgram(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return errors.New("the download is not a program")
	}
	ok := false
	switch runtime.GOOS {
	case "windows":
		ok = head[0] == 'M' && head[1] == 'Z'
	case "linux":
		ok = string(head) == "\x7fELF"
	case "darwin":
		magic := uint32(head[0])<<24 | uint32(head[1])<<16 | uint32(head[2])<<8 | uint32(head[3])
		ok = magic == 0xfeedfacf || magic == 0xcffaedfe || magic == 0xcafebabe
	}
	if !ok {
		return errors.New("the download is not a program for " + runtime.GOOS + ": nothing replaced")
	}
	return nil
}

// The file an update on Windows moved aside, gone at the next start
func dropOldBuild() {
	if runtime.GOOS != "windows" {
		return
	}
	if self, err := os.Executable(); err == nil {
		os.Remove(self + ".old")
	}
}
