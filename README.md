# codemap

A CLI tool that analyzes Go codebases and generates a small set of codemap outputs for fast navigation:

- `CODEMAP.paths`: token-efficient package → entry file routing (best for agents)
- `CODEMAP.md`: human-friendly summary (kept small)

## Installation

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

The installer also adds `.codemap.state.json` to the target repo `.gitignore`.

## Performance Tracking

You can track codemap performance over time using built-in benchmarks:

```bash
./scripts/perf-record.sh
./scripts/perf-report.sh
```

This records benchmark history in `perf/history.csv` and stores raw benchmark outputs in `perf/history/`.

CI also runs codemap benchmarks via `.github/workflows/perf-bench.yml` and publishes artifacts per run.
For persistent in-repo trend lines, run `./scripts/perf-record.sh` and commit the updated `perf/history.csv`.

## Excluded Directories

The following directories are automatically excluded:

- Hidden directories (starting with `.`)
- `vendor`
- `testdata`
- `workspace`

## License

MIT
