# codemap

A CLI tool that analyzes Go, Rust, and TypeScript codebases and generates a small set of codemap outputs for fast navigation:

- `CODEMAP.paths`: token-efficient package → entry file routing (best for agents)
- `CODEMAP.md`: human-friendly summary (kept small)

## Installation

`codemap` is implemented in Go. Building from source or using `go install` requires a Go toolchain (see `go.mod` for the current minimum version).
Rust and TypeScript symbol extraction uses Tree-sitter via CGO, so builds also require a working C toolchain.

```bash
go install github.com/Someblueman/codemap@latest
```

Or build from source:

```bash
git clone https://github.com/Someblueman/codemap
cd codemap
go build -o codemap
```

## Usage

```bash
# Generate/update outputs in current directory (only writes if stale)
codemap

# Generate for specific project
codemap -root /path/to/project

# Check staleness only (exit 1 if stale, 0 if up to date)
codemap -check

# Force regeneration even if up to date
codemap -force

# Custom markdown output path
codemap -output ARCHITECTURE.md

# Custom paths output path
codemap -paths-output ROUTES.paths

# Include test files
codemap -tests

# Disable CODEMAP.paths output
codemap -no-paths

# Verbose output
codemap -v
```

## One-Time Setup (Recommended)

Install a pre-commit hook and add agent guidance to `AGENTS.md` / `CLAUDE.md`:

```bash
# Run from inside the target repo
/path/to/codemap/scripts/install.sh
```

`codemap` must already be installed and available in `PATH` (the installer will refuse to proceed otherwise).

## Output

The generated outputs include:

- `CODEMAP.paths`: One line per package with a suggested entry file (and optional short purpose).
- `CODEMAP.md`: A small summary table with package entry points, plus a brief concern count summary.
- `.codemap.state.json`: Local incremental hash cache used to speed up `codemap -check` and unchanged runs.
- `.codemap.state.analysis.json`: Local package-analysis cache used to speed up repeated language analysis.

Example output:

```markdown
<!-- codemap-hash: a1b2c3d4... -->
<!-- Generated: 2026-01-17 10:30:00 UTC -->
<!-- Regenerate: codemap -->

# Codemap

## Package Entry Points

| Package | Entry File | Purpose |
|---------|------------|---------|
| internal/supervisor | internal/supervisor/supervisor.go | Agent orchestration |
...
```

## Pre-commit Hook

To keep `CODEMAP.paths` / `CODEMAP.md` updated automatically, install the provided hook:

```bash
# Run from inside the target repo
/path/to/codemap/scripts/install.sh
```

Or copy the hook template:

```bash
cp /path/to/codemap/scripts/pre-commit .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
```

The installer also adds `.codemap.state.json` and `.codemap.state.analysis.json` to the target repo `.gitignore`.
If `CODEMAP.md` / `CODEMAP.paths` are ignored, the hook still refreshes them locally and skips staging.

## Performance Tracking

You can track `codemap` performance over time using built-in synthetic benchmarks that exercise Go, Rust, and TypeScript fixture repos:

```bash
./scripts/perf-record.sh
./scripts/perf-report.sh
```

This records benchmark history in `perf/history.csv` and stores raw benchmark outputs in `perf/history/`.
If you run `perf-record.sh` outside this repo (or on a machine without Go), it exits cleanly and skips recording.

CI also runs codemap benchmarks via `.github/workflows/perf-bench.yml` and publishes artifacts per run.
For persistent in-repo trend lines, this repo also has a weekly cadence workflow:

- `.github/workflows/perf-history-cadence.yml` runs every Monday at 14:00 UTC.
- It records a benchmark sample and opens/updates a PR with `perf/history.csv`.
- You can also trigger it manually from GitHub Actions (`workflow_dispatch`).

You can still run `./scripts/perf-record.sh` locally at any time for ad-hoc sampling.

## Impact Measurement (Experimental)

Use the built-in impact report script to measure codemap usage patterns across repos:

```bash
python3 ./scripts/impact-report.py --repo /path/to/repo --since 2026-01-01
```

This report tracks:

- Codemap adoption rate (how often sessions touch `CODEMAP.paths` / `CODEMAP.md`)
- Early-touch rate (whether codemap is used in the first few actions)
- Speed-to-first-edit proxy (actions before first edit)
- File-open proxy (unique `Read` paths before first edit; Claude logs)
- Optional success metric from labeled outcomes

You can include labeled outcomes to compute success-rate deltas:

```csv
source,session_id,success
codex,019c4062-52f6-7601-a890-196d3b2a241a,1
claude,7d2abdc1-f1c3-4f4b-b3cd-2cc75b2fb6f9,0
```

```bash
python3 ./scripts/impact-report.py --repo /path/to/repo --outcomes-csv outcomes.csv
```

### Local Scheduled Collection (Cross-Repo)

Because impact metrics rely on local session logs (for example `~/.codex/sessions`), schedule collection on your own machine:

```bash
./scripts/install-impact-launchd.sh --hour 9 --minute 0 --since-days 30 --run-now
```

What this sets up:

- Launch agent label: `com.someblueman.codemap.impact-collect`
- Repo list file: `~/.codemap/impact/repos.txt` (one absolute path per line)
- Output snapshots: `perf/impact/<repo-name>.json`
- Run manifest: `perf/impact/latest-run.json`

Useful commands:

```bash
launchctl print gui/$(id -u)/com.someblueman.codemap.impact-collect
./scripts/install-impact-launchd.sh --uninstall
```

Repo list format example: `perf/impact-repos.example.txt`

## Excluded Directories

The following directories are automatically excluded:

- Hidden directories (starting with `.`)
- `vendor`
- `testdata`
- `workspace`
- `node_modules`

## License

MIT
