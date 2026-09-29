package yur

import (
	"path"
	"regexp"
	"strings"
)

type Secret struct {
	Path string
	Line int
	Kind string
}

var secretPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN (RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY( BLOCK)?-----`)},
	{"Yurba token", regexp.MustCompile(`\b(y|yp|b|a|oa|or)\.[A-Za-z0-9]{32}\b`)},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}\b|\bgithub_pat_[A-Za-z0-9_]{60,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[0-9A-Za-z-]{10,}`)},
	{"Stripe key", regexp.MustCompile(`\b(sk|rk)_live_[0-9A-Za-z]{20,}`)},
	{"Telegram bot token", regexp.MustCompile(`\b[0-9]{8,10}:AA[0-9A-Za-z_-]{33}`)},
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{30,}`)},
	{"OpenAI key", regexp.MustCompile(`\bsk-(proj-|svcacct-|admin-)?[A-Za-z0-9_-]{40,}`)},
}

// Files that are secrets by their name alone, whatever is in them
func SecretFile(p string) bool {
	name := strings.ToLower(path.Base(p))
	switch name {
	case ".env", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", ".npmrc", ".pypirc", "credentials.json", "service-account.json":
		return true
	}
	if strings.HasPrefix(name, ".env.") && !sampleName(name) {
		return true
	}
	return strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".p12") || strings.HasSuffix(name, ".pfx") || strings.HasSuffix(name, ".keystore")
}

// A file meant to be copied and filled in (.env.example, config.sample.json): by its own name only, so a
// folder called templates doesn't hide real keys
func sampleName(name string) bool {
	for _, word := range []string{"example", "sample", "template", "dist"} {
		if strings.Contains(name, "."+word) || strings.HasPrefix(name, word+".") || strings.Contains(name, "_"+word) || strings.Contains(name, "-"+word) {
			return true
		}
	}
	return false
}

// Keys and tokens written into a file. A sample file (.env.example) is left alone.
func FindSecrets(p string, data []byte) []Secret {
	found := []Secret{}
	if sampleName(strings.ToLower(path.Base(p))) {
		return found
	}
	if SecretFile(p) {
		return append(found, Secret{Path: p, Kind: "secret file"})
	}
	if len(data) > 2<<20 || IsBinary(data) {
		return found
	}
	for i, line := range strings.Split(string(data), "\n") {
		for _, s := range secretPatterns {
			if realSecret(s.re.FindAllString(line, -1)) {
				found = append(found, Secret{Path: p, Line: i + 1, Kind: s.kind})
				break
			}
		}
	}
	return found
}

// Docs show keys as "y.XXXXXXXX…": a run of one character is a placeholder, not a key
func realSecret(matches []string) bool {
	for _, m := range matches {
		if len(m) < 16 {
			return true
		}
		tail := m[len(m)-16:]
		if strings.Count(tail, tail[:1]) != len(tail) {
			return true
		}
	}
	return false
}
