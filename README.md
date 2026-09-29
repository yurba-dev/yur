# yur

yur is the version control of [Yurba Projects](https://yurba.one/projects). It keeps every version of your
code and sends it to Yurba. There is no staging area and nothing to stash: whatever you type is kept, and any
step, a merge or a sync too, is taken back with `yur undo`.

This repository holds the command line (`cmd/yur`) and the core it is built on (the package at the root),
which the Yurba server uses too, so both read and write the same format.

## Install

Ready builds for Windows, Linux and macOS are on [yurba.one/projects/yur](https://yurba.one/projects/yur).
Put the file somewhere in `PATH` (on Linux and macOS: `chmod +x yur`).

Or build it from the source with Go 1.21 or newer:

```sh
go build -o yur ./cmd/yur
```

## Start

```sh
yur login                     # sign in through the browser, once
yur init                      # make the folder a project
yur save -m "First version"   # save the changes as a rev
yur create                    # put the project on Yurba
yur sync                      # take the others' changes and give yours, in one step
```

A project already on Yurba comes with `yur clone <owner>/<name>`. A git repository comes in, with all its
history and tags, through `yur import-git`.

## Every day

```sh
yur status                  # what changed, and how far from the server
yur diff                    # the changes line by line
yur log                     # history
yur switch -c feature       # a new branch that takes the unsaved work along
yur propose                 # push the branch and open a proposal
yur release v1.0.0          # a version to download and to link to on the CDN
yur revert <rev>            # take back the changes of one rev
yur undo                    # take back the last command
yur help                    # all the commands
```

## What makes it easier than git

- **No staging, no stash.** The folder is always saved as unsaved work of its branch, so switching branches
  never loses or mixes anything.
- **Undo for everything.** `yur undo` takes back any command, and `yur ops` lists them all.
- **Changes keep their id.** A change id stays the same when a rev is amended or moved, so a proposal follows
  its changes through every rewrite.
- **Conflicts don't block.** They are kept in the rev and settled when you are ready.
- **Light clone.** The latest files come at once, the old ones when needed. Big files are split and stored
  once, with no extra tools.
- **Keys are caught.** Keys and passwords are found before they are saved and before they reach the server.
- **Verified author.** The author is a Yurba account, confirmed by the push itself.
- **In from git and back.** `yur import-git` brings a git repository in, `.gitignore` is read as it is, and
  `yur export-git` writes the history back into git.

## Settings

| Variable | What it does |
| --- | --- |
| `YUR_API` | The Yurba API to talk to (default `https://api.yurba.one`) |
| `YUR_WEB` | The Yurba site, for links and sign-in (default `https://yurba.one`) |
| `YUR_TOKEN` | A token to use instead of the one `yur login` saved, for a CI |
| `NO_COLOR` | Plain output without colors |

The settings `yur login` and `yur config` make are kept in `~/.yur/config.json`; the variables above are
never written there. Files to leave out go in `.gitignore` or `.yurignore` (`yur ignore <pattern>`).

## Layout

| Path | What is there |
| --- | --- |
| `object.go`, `tree.go`, `store.go` | Objects (files, trees, revs), their hashes and storage |
| `pack.go`, `packstore.go` | Packs: sending objects and keeping many of them in one file |
| `history.go` | Walking revs: merge bases, what one side has that the other lacks |
| `diff.go`, `merge.go`, `blame.go` | Line diffs, three-way merges, who brought each line |
| `secrets.go` | Finding keys and passwords in the changes |
| `cmd/yur` | The command line |

## Tests

```sh
go vet ./...
go test ./...
```

## License

MIT, see [LICENSE](LICENSE).
