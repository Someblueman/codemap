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

CI artifacts are per-run snapshots. For long-term in-repo trends, run `./scripts/perf-record.sh` locally and commit updated `perf/history.csv`.
