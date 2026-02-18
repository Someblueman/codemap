# Performance Tracking

This directory tracks `codemap` benchmark trends over time.

## What is measured

Benchmarks in `perf_benchmark_test.go`:

- `BenchmarkCodemapIsStaleWarm`: `IsStale` on an unchanged repository with state cache.
- `BenchmarkCodemapEnsureUpToDateWarm`: `EnsureUpToDate` on an unchanged repository.
- `BenchmarkCodemapEnsureUpToDateOnChange`: `EnsureUpToDate` after a source-file change.

## Record a run

```bash
./scripts/perf-record.sh
```

Optional environment variables:

- `BENCH_PATTERN` (default: `^BenchmarkCodemap`)
- `BENCH_TIME` (default: `1s`)
- `BENCH_COUNT` (default: `3`)

Outputs:

- `perf/history.csv`: append-only summary rows for trend tracking.
- `perf/history/bench-<timestamp>-<sha>.txt`: raw benchmark output.

## Compare latest vs previous

```bash
./scripts/perf-report.sh
```

This prints latest `ns/op` and delta against the previous sample per benchmark.

## CI

GitHub Actions workflow `.github/workflows/perf-bench.yml` runs these benchmarks on pull requests, `main` pushes, and manual dispatch, then uploads benchmark artifacts.

For persistent trend updates in git, workflow `.github/workflows/perf-history-cadence.yml` runs weekly (Monday 14:00 UTC) and opens/updates a PR with the new `perf/history.csv` sample.

CI artifacts remain per-run snapshots. You can still run `./scripts/perf-record.sh` locally for ad-hoc checks.

## Impact Collection Cadence (Local Machine)

Use local scheduling for impact metrics because session logs live on your machine (`~/.codex/sessions`, `~/.claude/projects`).

Install a daily launchd job:

```bash
./scripts/install-impact-launchd.sh --hour 9 --minute 0 --since-days 30 --run-now
```

Collector behavior:

- Reads repos from `~/.codemap/impact/repos.txt`
- Writes per-repo JSON snapshots to `perf/impact/`
- Writes run metadata to `perf/impact/latest-run.json`

Example repo list format: `perf/impact-repos.example.txt`
