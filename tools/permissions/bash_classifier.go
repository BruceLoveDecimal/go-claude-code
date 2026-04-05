package permissions

import (
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Bash Safety Classifier
// ─────────────────────────────────────────────────────────────────────────────

// BashClassification is the result of ClassifyBash.
type BashClassification struct {
	// IsDangerous is true when the command matches one or more danger patterns.
	IsDangerous bool
	// Warning is a human-readable explanation of the danger (empty when safe).
	Warning string
	// MatchedPatterns lists the names of the matched danger patterns.
	MatchedPatterns []string
}

// dangerPattern pairs a compiled regex with a short human-readable label.
type dangerPattern struct {
	label string
	re    *regexp.Regexp
}

// dangPatterns is the list of dangerous command patterns checked by
// ClassifyBash.  It mirrors the yoloClassifier / bashClassifier logic in the
// TypeScript source.
var dangPatterns = []dangerPattern{
	// Recursive remove
	{label: "recursive-delete", re: regexp.MustCompile(`\brm\s+(-[a-zA-Z]*r[a-zA-Z]*|-r[a-zA-Z]*)\s`)},
	// Force remove
	{label: "force-delete", re: regexp.MustCompile(`\brm\s+(-[a-zA-Z]*f[a-zA-Z]*|--force)\b`)},
	// Disk wipe / zero-fill
	{label: "dd-wipe", re: regexp.MustCompile(`\bdd\b.*\bof=`)},
	// Low-level disk format
	{label: "mkfs", re: regexp.MustCompile(`\bmkfs\b`)},
	// File system check with repair (can truncate files)
	{label: "fsck-repair", re: regexp.MustCompile(`\bfsck\b.*(-a|-y|--repair)\b`)},
	// Drop SQL tables/databases
	{label: "sql-drop", re: regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema)\b|truncate\s+table\b)`)},
	// Broad chmod (world-writable)
	{label: "chmod-world-write", re: regexp.MustCompile(`\bchmod\b.*(777|a\+w|o\+w)`)},
	// chown to root
	{label: "chown-root", re: regexp.MustCompile(`\bchown\b.*\broot\b`)},
	// wget/curl piped directly to sh/bash (arbitrary code execution)
	{label: "curl-pipe-sh", re: regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(bash|sh|zsh|fish|ksh)\b`)},
	// Direct /dev/sda writes
	{label: "raw-disk-write", re: regexp.MustCompile(`\b(of|>\s*)/dev/[sh]d[a-z]`)},
	// Shutdown/reboot
	{label: "shutdown", re: regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`)},
	// Kill all processes
	{label: "killall", re: regexp.MustCompile(`\bkill\b\s+-9\s+-1\b|\bkillall\b\s+-9\b`)},
	// History deletion
	{label: "history-delete", re: regexp.MustCompile(`\bhistory\s+-c\b|\brm\b.*bash_history`)},
	// /etc/passwd or /etc/shadow manipulation
	{label: "shadow-passwd-write", re: regexp.MustCompile(`\b(>\s*|tee\s+)/etc/(passwd|shadow|sudoers)\b`)},
	// git push --force to main/master
	{label: "force-push-main", re: regexp.MustCompile(`\bgit\b.*push\b.*(--force|-f)\b.*\b(main|master)\b`)},
	// eval of arbitrary variable
	{label: "eval-injection", re: regexp.MustCompile(`\beval\s+\$`)},
	// base64-decode piped to sh (encoded payload execution)
	{label: "base64-exec", re: regexp.MustCompile(`\bbase64\b.*-d.*\|\s*(bash|sh)\b`)},
}

// ClassifyBash checks a shell command string for dangerous patterns.
// It is called by the permissions layer even in bypassPermissions mode to
// surface warnings to the user before execution.
//
// ClassifyBash does NOT block execution; it only provides a classification.
// The caller decides whether to warn, confirm, or block based on the result.
func ClassifyBash(cmd string) BashClassification {
	if cmd == "" {
		return BashClassification{}
	}

	// Normalise whitespace for easier matching.
	normalised := strings.Join(strings.Fields(cmd), " ")

	var result BashClassification
	for _, p := range dangPatterns {
		if p.re.MatchString(normalised) {
			result.IsDangerous = true
			result.MatchedPatterns = append(result.MatchedPatterns, p.label)
		}
	}

	if result.IsDangerous {
		result.Warning = buildWarning(result.MatchedPatterns)
	}
	return result
}

func buildWarning(patterns []string) string {
	if len(patterns) == 1 {
		return "Potentially dangerous command detected (" + patterns[0] + "). Proceed with caution."
	}
	return "Potentially dangerous command detected (" + strings.Join(patterns, ", ") + "). Proceed with caution."
}
