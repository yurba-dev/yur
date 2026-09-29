package yur

import (
	"path"
	"strings"
)

type Language struct {
	Name  string
	Color string
	// The name highlight.js knows it by
	Code string
}

var languages = map[string]Language{
	".go":     {"Go", "#00ADD8", "go"},
	".js":     {"JavaScript", "#f1e05a", "javascript"},
	".mjs":    {"JavaScript", "#f1e05a", "javascript"},
	".cjs":    {"JavaScript", "#f1e05a", "javascript"},
	".jsx":    {"JavaScript", "#f1e05a", "javascript"},
	".ts":     {"TypeScript", "#3178c6", "typescript"},
	".tsx":    {"TypeScript", "#3178c6", "typescript"},
	".slim":   {"Slim", "#7b5cff", "javascript"},
	".py":     {"Python", "#3572A5", "python"},
	".php":    {"PHP", "#4F5D95", "php"},
	".rb":     {"Ruby", "#701516", "ruby"},
	".java":   {"Java", "#b07219", "java"},
	".kt":     {"Kotlin", "#A97BFF", "kotlin"},
	".kts":    {"Kotlin", "#A97BFF", "kotlin"},
	".swift":  {"Swift", "#F05138", "swift"},
	".c":      {"C", "#555555", "c"},
	".h":      {"C", "#555555", "c"},
	".cpp":    {"C++", "#f34b7d", "cpp"},
	".cc":     {"C++", "#f34b7d", "cpp"},
	".hpp":    {"C++", "#f34b7d", "cpp"},
	".cs":     {"C#", "#178600", "csharp"},
	".rs":     {"Rust", "#dea584", "rust"},
	".dart":   {"Dart", "#00B4AB", "dart"},
	".lua":    {"Lua", "#000080", "lua"},
	".sh":     {"Shell", "#89e051", "bash"},
	".bash":   {"Shell", "#89e051", "bash"},
	".ps1":    {"PowerShell", "#012456", "powershell"},
	".html":   {"HTML", "#e34c26", "xml"},
	".htm":    {"HTML", "#e34c26", "xml"},
	".vue":    {"Vue", "#41b883", "xml"},
	".svelte": {"Svelte", "#ff3e00", "xml"},
	".css":    {"CSS", "#563d7c", "css"},
	".scss":   {"SCSS", "#c6538c", "scss"},
	".less":   {"Less", "#1d365d", "less"},
	".sql":    {"SQL", "#e38c00", "sql"},
	".json":   {"JSON", "#292929", "json"},
	".yaml":   {"YAML", "#cb171e", "yaml"},
	".yml":    {"YAML", "#cb171e", "yaml"},
	".toml":   {"TOML", "#9c4221", "ini"},
	".ini":    {"INI", "#d1dbe0", "ini"},
	".xml":    {"XML", "#0060ac", "xml"},
	".md":     {"Markdown", "#083fa1", "markdown"},
	".proto":  {"Protocol Buffers", "#4a90d9", "protobuf"},
	".gd":     {"GDScript", "#355570", "gdscript"},
	".r":      {"R", "#198CE7", "r"},
	".scala":  {"Scala", "#c22d40", "scala"},
	".ex":     {"Elixir", "#6e4a7e", "elixir"},
	".erl":    {"Erlang", "#B83998", "erlang"},
	".hs":     {"Haskell", "#5e5086", "haskell"},
	".zig":    {"Zig", "#ec915c", "zig"},
	".nim":    {"Nim", "#ffc200", "nim"},
	".pl":     {"Perl", "#0298c3", "perl"},
	".asm":    {"Assembly", "#6E4C13", "x86asm"},
	".bat":    {"Batchfile", "#C1F12E", "dos"},
}

var fileLanguages = map[string]Language{
	"dockerfile": {"Dockerfile", "#384d54", "dockerfile"},
	"makefile":   {"Makefile", "#427819", "makefile"},
}

func LanguageOf(p string) (Language, bool) {
	name := strings.ToLower(path.Base(p))
	if l, ok := fileLanguages[name]; ok {
		return l, true
	}
	l, ok := languages[path.Ext(name)]
	return l, ok
}

// Data and docs count for nothing in a project's languages, as they tell little of what it is written in
func CountsAsCode(p string) bool {
	l, ok := LanguageOf(p)
	if !ok {
		return false
	}
	switch l.Name {
	case "JSON", "Markdown", "YAML", "TOML", "INI", "XML":
		return false
	}
	for _, part := range SplitPath(p) {
		switch strings.ToLower(part) {
		case "node_modules", "vendor", "dist", "build", "third_party", ".min":
			return false
		}
	}
	return !strings.Contains(p, ".min.")
}
