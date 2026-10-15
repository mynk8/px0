package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The working set: checkouts an agent or the user said matter now. Stored with
// px0's other per-user files, never in a workspace, keyed by absolute path so
// any px0 serving a folder that contains the checkout shows it.

type setItem struct {
	Path  string `json:"path"`
	Note  string `json:"note,omitempty"`
	Added string `json:"added"`
}

type workingSet struct {
	Items []setItem `json:"items"`
}

func setFilePath() string {
	p := settingsPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "set.json")
}

func readSet() workingSet {
	var ws workingSet
	if b, err := os.ReadFile(setFilePath()); err == nil {
		json.Unmarshal(b, &ws)
	}
	return ws
}

func writeSet(ws workingSet) error {
	p := setFilePath()
	if p == "" {
		return errors.New("no home directory for the working set")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if ws.Items == nil {
		ws.Items = []setItem{}
	}
	b, _ := json.MarshalIndent(ws, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// checkoutTop resolves any path inside a checkout to the checkout's top level.
func checkoutTop(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git checkout", abs)
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

const setUsage = "usage: px0 set add|remove|list|clear [path] [-note text]"

// runSetCmd implements `px0 set`, the way agents and scripts say which
// checkouts matter now. The path defaults to the current directory.
func runSetCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(setUsage)
	}
	fs := flag.NewFlagSet("set "+args[0], flag.ContinueOnError)
	note := fs.String("note", "", "what the checkout is for")
	fs.SetOutput(io.Discard)
	// Accept the path before or after -note.
	rest := args[1:]
	var pos []string
	for {
		if err := fs.Parse(rest); err != nil {
			return fmt.Errorf("%v; %s", err, setUsage)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	target := "."
	if len(pos) > 0 {
		target = pos[0]
	}
	ws := readSet()
	switch args[0] {
	case "add":
		top, err := checkoutTop(target)
		if err != nil {
			return err
		}
		for i := range ws.Items {
			if ws.Items[i].Path == top {
				ws.Items[i].Note = *note
				return writeSet(ws)
			}
		}
		ws.Items = append(ws.Items, setItem{Path: top, Note: *note, Added: time.Now().UTC().Format(time.RFC3339)})
		return writeSet(ws)
	case "remove":
		top, err := checkoutTop(target)
		if err != nil {
			top, _ = filepath.Abs(target) // a deleted worktree can still be removed
		}
		kept := ws.Items[:0]
		for _, it := range ws.Items {
			if it.Path != top {
				kept = append(kept, it)
			}
		}
		ws.Items = kept
		return writeSet(ws)
	case "list":
		for _, it := range ws.Items {
			fmt.Fprintf(out, "%s\t%s\n", it.Path, it.Note)
		}
		return nil
	case "clear":
		return writeSet(workingSet{})
	}
	return fmt.Errorf("unknown set command %q; %s", args[0], setUsage)
}

var agentNames = map[string]bool{"claude": true, "codex": true, "gemini": true, "cursor-agent": true, "opencode": true, "aider": true, "goose": true, "agy": true}

// agentNameFromCmdline names the coding agent a process is, from its argv:
// the program itself, or the script a node/python/bun interpreter runs.
func agentNameFromCmdline(cmdline []byte) string {
	argv := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	name := func(s string) string {
		return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(s), ".js"), ".mjs")
	}
	if len(argv) == 0 || argv[0] == "" {
		return ""
	}
	if n := name(argv[0]); agentNames[n] {
		return n
	}
	switch name(argv[0]) {
	case "node", "bun", "python", "python3", "deno":
		if len(argv) > 1 {
			if n := name(argv[1]); agentNames[n] {
				return n
			}
		}
	}
	return ""
}

// agentCheckouts returns the checkouts a coding agent process is working in,
// by each process's cwd. Linux only (/proc); elsewhere nothing is found.
func agentCheckouts(root string, cos []checkout) map[string]bool {
	found := map[string]bool{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return found
	}
	for _, e := range ents {
		if n := e.Name(); n == "" || n[0] < '0' || n[0] > '9' {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || agentNameFromCmdline(cmd) == "" {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, cwd)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		best := ""
		for _, c := range cos {
			if (rel == c.Prefix || strings.HasPrefix(rel, c.Prefix+"/")) && len(c.Prefix) > len(best) {
				best = c.Prefix
			}
		}
		if best != "" {
			found[best] = true
		}
	}
	return found
}
