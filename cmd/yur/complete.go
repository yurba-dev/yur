package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Completion: the shell asks yur __complete what fits the word being typed, so branches and tags come
// from the project itself. Nothing printed leaves the shell to offer file names.

var completionScripts = map[string]string{
	"bash": `# yur completion bash > ~/.yur-completion.bash, then: source ~/.yur-completion.bash (in ~/.bashrc)
_yur() {
	local IFS=$'\n'
	COMPREPLY=($(yur __complete "--cur=${COMP_WORDS[COMP_CWORD]}" "${COMP_WORDS[@]:1:COMP_CWORD-1}" 2>/dev/null))
}
complete -o default -F _yur yur
`,
	"zsh": `#compdef yur
# yur completion zsh > "${fpath[1]}/_yur", then restart the shell
_yur() {
	local -a found
	found=("${(@f)$(yur __complete "--cur=${words[CURRENT]}" "${(@)words[2,CURRENT-1]}" 2>/dev/null)}")
	if (( ${#found} )) && [[ -n "${found[1]}" ]]; then
		compadd -a found
	else
		_files
	fi
}
compdef _yur yur
`,
	"powershell": `# yur completion powershell | Out-String | Invoke-Expression (in your $PROFILE)
Register-ArgumentCompleter -Native -CommandName yur -ScriptBlock {
	param($wordToComplete, $commandAst, $cursorPosition)
	$words = @($commandAst.CommandElements | Select-Object -Skip 1 | ForEach-Object { $_.ToString() })
	if ($wordToComplete -ne '' -and $words.Count -gt 0) { $words = @($words | Select-Object -SkipLast 1) }
	yur __complete "--cur=$wordToComplete" @words 2>$null | ForEach-Object {
		[System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
	}
}
`,
}

func cmdCompletion(_ *Repo, a *args) error {
	script, ok := completionScripts[a.arg(0)]
	if !ok {
		return errors.New("which shell? yur completion bash|zsh|powershell")
	}
	fmt.Print(script)
	return nil
}

// yur __complete --cur=<word> <the words before it>
func complete(raw []string) {
	cur, words := "", []string{}
	for _, w := range raw {
		if strings.HasPrefix(w, "--cur=") && cur == "" {
			cur = strings.TrimPrefix(w, "--cur=")
			continue
		}
		words = append(words, w)
	}
	for _, c := range candidates(words, cur) {
		if strings.HasPrefix(c, cur) {
			fmt.Println(c)
		}
	}
}

func candidates(words []string, cur string) []string {
	if len(words) == 0 {
		out := []string{}
		for _, c := range commands {
			out = append(out, c.name)
			out = append(out, c.aliases...)
		}
		sort.Strings(out)
		return out
	}
	c := find(words[0])
	if c == nil {
		return nil
	}
	prev := words[len(words)-1]
	switch {
	case c.name == "help":
		return candidates(nil, cur)
	case c.name == "completion":
		return []string{"bash", "zsh", "powershell"}
	case c.name == "check" && len(words) == 2:
		return []string{"pending", "success", "failure", "error"}
	case strings.HasPrefix(cur, "-"):
		return nil
	case c.name == "tag" && (prev == "-d" || prev == "--delete"):
		return names(false, true)
	case prev == "-r" || prev == "--rev" || prev == "--from" || prev == "--to":
		return names(true, true)
	}
	switch c.name {
	case "switch", "merge", "push":
		return names(true, false)
	case "branch":
		if prev == "-d" || prev == "--delete" || prev == "-m" {
			return names(true, false)
		}
	case "show", "revert", "pick", "diff", "checks":
		return names(true, true)
	}
	return nil
}

// Branches (the server's too) and tags of the project the shell is in
func names(branches bool, tags bool) []string {
	root, err := findRoot(".")
	if err != nil {
		return nil
	}
	r, err := loadRepo(root)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	if branches {
		for n := range r.State.Branches {
			seen[n] = true
		}
		if r.State.Remote != nil {
			for n := range r.State.Remote.Branches {
				seen[n] = true
			}
		}
	}
	if tags {
		for n := range r.State.Tags {
			seen[n] = true
		}
		if r.State.Remote != nil {
			for n := range r.State.Remote.Tags {
				seen[n] = true
			}
		}
	}
	out := []string{}
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
