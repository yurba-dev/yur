# Contributing

Tasks and proposals are welcome on [Yurba](https://yurba.one/projects/yurba/yur).

- Only the Go standard library is used; a change that needs another module should say why in its proposal.
- `go vet ./...` and `go test ./...` pass, and `gofmt` has run. A fix comes with a test that fails without it.
- The format of objects and packs is shared with the Yurba server: a change to it must still read what older
  versions wrote.
- Comments are few and in English, and say why rather than what.
- By sending a change you agree that it is published under the MIT license of this repository.
