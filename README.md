<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://px0.ai/logo/px0-logo-dark.png">
  <source media="(prefers-color-scheme: light)" srcset="https://px0.ai/logo/px0-logo-light.png">
  <img alt="px0" src="https://px0.ai/logo/px0-logo-dark.png" width="120">
</picture>

---

px0 is an IDE built for reviewing AI-generated code, optimized for speed. It turns your browser into a zero-latency console with native Git and GitHub integrations, instant search across massive codebases, and seamless handoff to local AI coding harnesses.

As AI agents author more code directly from the terminal, engineering productivity is no longer constrained by how fast you type - it is constrained by how fast you can review, navigate, and verify changes. px0 replaces heavy, multi-gigabyte editing suites with an instant, distraction-free environment built specifically for this review loop.

- < 1 ms cold start
- ~20–30 MB server daemon RAM (~100–180 MB total with browser tab, vs. VS Code's ~1,440 MB)
- Single static Go binary with zero runtime dependencies (no Electron, no Node, no CGO)
- ~280 languages tokenized natively via Chroma

See full performance benchmarks and comparisons at [px0.ai/benchmarks](https://px0.ai/benchmarks).

## Features

- GitHub PR reviews & Git panel: Review pull requests directly (`px0 <pr-url>`), inspect scoped merge-base diffs, draft inline review comments, expand recent commits to browse what each one changed, and stage, commit, or push from the browser.
- Unpushed commit review: Read the commits your branch is ahead of its tracking branch by, one file diff at a time, before you push them.
- AI coding harness integration: Dispatch edits directly to Claude Code, Gemini CLI, Cursor Agent, Antigravity, OpenCode, Codex, Aider, or Goose with live reloading.
- Fast navigation: Fuzzy file search, symbol outline, and workspace regex search in milliseconds.
- Remote-first: Run on any remote server, VM, or container and browse locally without SSH key setups or remote daemons.
- Virtual rendering: Opens 400,000-line files smoothly by mounting only visible rows; frees memory back to the OS after 15 seconds of inactivity.
- Rich code viewer: 14 built-in themes, rendered Markdown with Mermaid diagrams, CSV/TSV table view, image inspector, and optional zero-config LSP for Go-to-Definition and hover.

## Installation

Quick install (macOS, Linux, BSD):

```bash
curl -fsSL https://px0.ai/install.sh | sh
```

Build from source (requires Go 1.24+ and Node.js or Bun):

```bash
git clone https://github.com/px0-ai/px0.git
cd px0
make build
install -d ~/.local/bin && install px0 ~/.local/bin/
```

To update an existing installation:

```bash
px0 --update
```

## Usage

```bash
# Inspect current directory
px0

# Inspect a specific directory or file
px0 ~/workspace/project
px0 main.go:42

# Review a GitHub pull request
px0 https://github.com/owner/repo/pull/123

# Remote or headless server mode
px0 -host 0.0.0.0 -port 7777 ~/workspace

# Behind a reverse proxy under a subpath
px0 -base-path /rev-123/ -host 0.0.0.0 -port 7777 ~/workspace
```

When px0 binds to `0.0.0.0`, it prints a `network` URL for every unique
non-loopback IPv4 address on the machine, using the port selected by the
listener:

```text
  url:        http://0.0.0.0:7777
  network:    http://10.0.0.15:7777
  network:    http://192.168.1.42:7777
```

Open the address that is reachable from your local machine. The list can
include LAN, VPN, and container-network addresses, depending on the remote
host's interfaces.

Access securely over Tailscale, WireGuard, reverse proxy, or Cloudflare Tunnel
with zero remote setup overhead and strict read-only sandboxing
(path traversal protection & DNS rebinding checks).

## Workspace mode

Open a folder that is not itself a git repo but holds many repos and linked worktrees (for example `px0 ~/workspace`). px0 finds every checkout inside it and turns git on for all of them: status badges, side-by-side diffs, gutter markers, commit history and staging work for any file in any checkout. Nothing is written into the folder.

- **Explorer views.** The explorer lists checkouts grouped by repo, each worktree under its main checkout with its branch, uncommitted changes and unpushed commits.
  - **Working set** (the default) shows the checkouts a coding agent is running in right now, plus any added with `px0 set add`.
  - **Changes** shows the checkouts with changes.
  - **All** shows everything.
- **Scope.** The target button on a checkout row scopes px0 to it: the tree, quick open, search and the git panel. ✕ in the bar under the header goes back to all checkouts.
- **Git panel.** It acts on the checkout of the open file, or on the scoped one.
- **Compare against.** In the diff menu, *Uncommitted* is the working tree against HEAD. *This branch* is everything since the branch's parent: the feature branch it tracks, else the repo's default branch. It shows one layer of a stack on its own.
- **Coding agent.** Edits and threads run the harness inside the owning checkout, so it reads that repo's own `AGENTS.md` / `CLAUDE.md`.

The working set lives in `~/.px0/set.json`. Agents and scripts manage it from the command line, and a running px0 updates within a second:

```bash
px0 set add . -note "what this checkout is for"   # the checkout containing the current directory
px0 set remove ~/workspace/repo-feature-x
px0 set list
px0 set clear
```

Agent detection reads process working directories from `/proc`, so it works on Linux only.

**Pull requests.** px0's PR review (the PR bar, line comments with Alt+R, the Comments panel, replies, Submit / Approve / Request changes) follows the active checkout: open a file in a checkout whose branch has an open PR and its review UI appears. The palette's *Open Pull Request…* opens any PR as a review checkout beside its repo (`<repo>-review-pr<N>`, a git worktree of your local clone, matched through `origin` or `upstream`), in *This branch* mode so the PR's own diff shows. *Remove checkout* in the PR bar deletes it again; a checkout with changes is kept.

## Development

```bash
git clone https://github.com/px0-ai/px0.git
cd px0

# Run tests
make test

# Live frontend development (serves web/ assets directly from disk)
go run . -dev .

# Build binary
make build
```

## Benchmarks

```bash
# Clone the benchmark corpus (~3 GB: Linux, Kubernetes, TypeScript, etc.)
./benchmark.sh --clone

# Run benchmarks across the corpus
./benchmark.sh

# Compare px0 directly against VS Code on the current workspace
./benchmark.sh --vscode .

# Profile memory lifecycle (index, search, idle memory release)
./benchmark.sh --memory bench-repos/linux
```

> **Architecture & Memory Accounting**: px0 separates the lightweight native Go daemon from the browser-rendered UI. The Go server consumes ~20–30 MB resident RAM (RSS). The active browser tab allocates ~80–150 MB for DOM nodes, V8 JS runtime, and GPU compositing. Combined, the total local memory footprint is ~100–180 MB (~85–90% lighter than Electron IDEs like VS Code at ~1,440 MB, which bundle dedicated Chromium and Node runtimes). In remote or container environments (`px0 -host 0.0.0.0`), the remote host pays strictly the ~20–30 MB server cost. See [BENCHMARKS.md](BENCHMARKS.md) for full methodology and breakdowns.

## Documentation

- [Docs](https://px0.ai/docs)
- [Benchmarks](https://px0.ai/#bench)
- [Blog](https://px0.ai/blog)
- [Changelog](https://px0.ai/changelog)
- [Partners](https://px0.ai/design-partners)

## Design Partners

When AI coding agents generate large diffs across repositories every day, code authoring is no longer the bottleneck. Verification and review are.

We are looking for engineering teams (10+ engineers or teams running active agent workflows) as design partners. Partners receive direct Slack Connect access to the core team, rapid turnaround on custom harness integrations, and direct input on the roadmap.

Learn more on the [Design Partners page](https://px0.ai/design-partners).

## License

[MIT License](LICENSE) © Arpit Bhayani
