// Package prompt builds the system prompt sent with every API request.
// It mirrors the sectioned system-prompt construction in the TypeScript source
// (src/constants/prompts.ts and src/services/systemPrompt.ts).
//
// The core contract is prompt.Build(cfg) → string.  Callers (engine.go) call
// Build once per turn and pass the result as api.StreamParams.SystemPrompt.
package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/claude-code/go-claude-go/tools"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// BuildConfig carries everything the builder needs to produce a system prompt.
type BuildConfig struct {
	// CWD is the working directory shown to the model and used to locate CLAUDE.md.
	CWD string

	// Registry provides tool descriptions injected into the tools section.
	// May be nil — tools section is skipped.
	Registry *tools.Registry

	// BaseSystemPrompt is literal text inserted as the first section.
	// If empty, a concise default identity paragraph is used.
	BaseSystemPrompt string

	// AppendSystemPrompt is appended verbatim after all generated sections.
	AppendSystemPrompt string

	// DisableEnvDetection suppresses the environment section (useful in tests).
	DisableEnvDetection bool

	// DisableClaudeMD suppresses CLAUDE.md injection (useful in tests).
	DisableClaudeMD bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Build — public entry point
// ─────────────────────────────────────────────────────────────────────────────

// Build assembles the full system prompt from the provided config.  The result
// is a single string separated into sections by blank lines.
func Build(cfg BuildConfig) string {
	var sb sectionBuilder

	// ── 1. Base identity / instructions ──────────────────────────────────────
	base := cfg.BaseSystemPrompt
	if base == "" {
		base = defaultIdentity
	}
	sb.add("", base)

	// ── 2. Environment section ────────────────────────────────────────────────
	if !cfg.DisableEnvDetection {
		sb.add("<env>", buildEnvSection(cfg.CWD))
	}

	// ── 3. CLAUDE.md project directives ──────────────────────────────────────
	if !cfg.DisableClaudeMD {
		if claudeMD := loadClaudeMD(cfg.CWD); claudeMD != "" {
			sb.add("<user_instructions>", claudeMD)
		}
	}

	// ── 4. Tool descriptions supplement ──────────────────────────────────────
	if cfg.Registry != nil {
		if toolDesc := buildToolsSection(cfg.Registry); toolDesc != "" {
			sb.add("<tools>", toolDesc)
		}
	}

	// ── 5. Caller-supplied suffix ─────────────────────────────────────────────
	if cfg.AppendSystemPrompt != "" {
		sb.add("", cfg.AppendSystemPrompt)
	}

	return sb.build()
}

// ─────────────────────────────────────────────────────────────────────────────
// Default identity paragraph
// ─────────────────────────────────────────────────────────────────────────────

const defaultIdentity = `You are Claude Code, an AI assistant specialized in software engineering.
You are running inside a terminal-based agentic loop.  You have access to tools
that let you read and write files, run shell commands, search codebases, and
coordinate with subagents.

Guidelines:
- Be concise and direct.  Lead with the answer or action.
- Prefer editing existing files over creating new ones.
- Do not add unnecessary comments, docstrings, or type annotations.
- Only add error handling for plausible failure modes at system boundaries.
- If unsure about scope, ask the user rather than guessing.
- Commit code only when the user explicitly requests it.`

// ─────────────────────────────────────────────────────────────────────────────
// Environment section
// ─────────────────────────────────────────────────────────────────────────────

func buildEnvSection(cwd string) string {
	var lines []string

	// Operating system
	lines = append(lines, fmt.Sprintf("Operating system: %s (%s)", runtime.GOOS, runtime.GOARCH))

	// Shell
	shell := os.Getenv("SHELL")
	if shell == "" && runtime.GOOS == "windows" {
		shell = os.Getenv("COMSPEC")
	}
	if shell == "" {
		shell = "(unknown)"
	}
	lines = append(lines, fmt.Sprintf("Shell: %s", shell))

	// Working directory
	effectiveCWD := cwd
	if effectiveCWD == "" {
		if d, err := os.Getwd(); err == nil {
			effectiveCWD = d
		}
	}
	if effectiveCWD != "" {
		lines = append(lines, fmt.Sprintf("Working directory: %s", effectiveCWD))
	}

	// Git context
	if gitInfo := detectGit(effectiveCWD); gitInfo != "" {
		lines = append(lines, gitInfo)
	}

	// Current date/time (UTC)
	lines = append(lines, fmt.Sprintf("Date/time (UTC): %s", time.Now().UTC().Format("2006-01-02 15:04 MST")))

	return strings.Join(lines, "\n")
}

// detectGit returns a string describing the git state (root + branch) or ""
// if the directory is not inside a git repo.
func detectGit(dir string) string {
	if dir == "" {
		return ""
	}

	// git rev-parse --show-toplevel
	rootCmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	rootOut, err := rootCmd.Output()
	if err != nil {
		return "" // not a git repo
	}
	gitRoot := strings.TrimSpace(string(rootOut))

	// git branch --show-current (git ≥ 2.22)
	branchCmd := exec.Command("git", "-C", dir, "branch", "--show-current")
	branchOut, _ := branchCmd.Output()
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		// Detached HEAD — show short commit hash instead
		hashCmd := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
		hashOut, _ := hashCmd.Output()
		branch = "HEAD@" + strings.TrimSpace(string(hashOut))
	}

	return fmt.Sprintf("Git root: %s  branch: %s", gitRoot, branch)
}

// ─────────────────────────────────────────────────────────────────────────────
// CLAUDE.md loader
// ─────────────────────────────────────────────────────────────────────────────

// loadClaudeMD searches from dir upward to the git root (or filesystem root)
// for CLAUDE.md files, returning their concatenated contents.  Files closer to
// the git root appear first (lower specificity), files closer to CWD appear
// last (higher specificity), so later instructions take precedence.
func loadClaudeMD(dir string) string {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	dir = filepath.Clean(dir)

	// Collect candidates by walking upward from dir to git root (or /).
	var candidates []string
	cursor := dir
	gitRoot := findGitRoot(dir)

	for {
		p := filepath.Join(cursor, "CLAUDE.md")
		if _, err := os.Stat(p); err == nil {
			candidates = append(candidates, p)
		}

		// Stop at git root or filesystem root.
		if cursor == gitRoot || isFilesystemRoot(cursor) {
			break
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
		cursor = parent
	}

	if len(candidates) == 0 {
		return ""
	}

	// Reverse so git-root file comes first, cwd file comes last.
	for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}

	var parts []string
	for _, path := range candidates {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(content))
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("# From %s\n\n%s", path, text))
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// findGitRoot returns the git repository root for dir, or dir itself if no
// git root is found.
func findGitRoot(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return dir
	}
	return strings.TrimSpace(string(out))
}

func isFilesystemRoot(path string) bool {
	return path == "/" || (len(path) == 3 && path[1] == ':' && path[2] == '\\')
}

// ─────────────────────────────────────────────────────────────────────────────
// Tools section
// ─────────────────────────────────────────────────────────────────────────────

// buildToolsSection produces a brief supplementary description of non-obvious
// tools so the model can make better use of them.  This is intentionally terse —
// the full JSON schema for each tool is already sent in the tools array.
func buildToolsSection(reg *tools.Registry) string {
	ts := reg.Enabled()
	if len(ts) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("You have %d tools available:", len(ts)))
	for _, t := range ts {
		lines = append(lines, fmt.Sprintf("  - %s: %s", t.Name(), t.Description()))
	}
	return strings.Join(lines, "\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// Section builder helper
// ─────────────────────────────────────────────────────────────────────────────

type sectionBuilder struct {
	parts []string
}

// add appends a section.  If tag is non-empty the content is wrapped in an
// XML-like tag (e.g. "<env>…</env>").  If tag is empty the content is appended
// verbatim.
func (b *sectionBuilder) add(tag, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	if tag == "" {
		b.parts = append(b.parts, content)
		return
	}
	// Derive closing tag: "<env>" → "</env>"
	closeTag := "</" + strings.TrimLeft(tag, "<") // already has ">"
	b.parts = append(b.parts, tag+"\n"+content+"\n"+closeTag)
}

func (b *sectionBuilder) build() string {
	return strings.Join(b.parts, "\n\n")
}
