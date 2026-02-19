#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "${root}"

if ! command -v go >/dev/null 2>&1; then
  echo "error: Go toolchain is required." >&2
  exit 1
fi

benchstat_bin="${BENCHSTAT_BIN:-benchstat}"
if ! command -v "${benchstat_bin}" >/dev/null 2>&1; then
  echo "error: benchstat not found in PATH." >&2
  echo "hint: go install golang.org/x/perf/cmd/benchstat@latest" >&2
  exit 1
fi

base_ref="${1:-origin/main}"
if ! git rev-parse --verify "${base_ref}" >/dev/null 2>&1; then
  echo "error: unable to resolve base ref ${base_ref}" >&2
  exit 1
fi

bench_pattern="${BENCH_PATTERN:-^BenchmarkCodemap}"
bench_time="${BENCH_TIME:-250ms}"
bench_count="${BENCH_COUNT:-5}"

detect_benchmark_package() {
  local repo_dir="$1"
  if [[ -f "${repo_dir}/internal/codemap/perf_benchmark_test.go" ]]; then
    printf "./internal/codemap"
    return
  fi
  if [[ -f "${repo_dir}/perf_benchmark_test.go" ]]; then
    printf "."
    return
  fi
  printf "./..."
}

base_commit="$(git rev-parse "${base_ref}")"
base_short="$(git rev-parse --short "${base_commit}")"
head_short="$(git rev-parse --short HEAD)"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"

out_dir="${BENCHSTAT_OUT_DIR:-perf/history}"
mkdir -p "${out_dir}"

base_raw="${out_dir}/bench-base-${base_short}-${timestamp}.txt"
head_raw="${out_dir}/bench-head-${head_short}-${timestamp}.txt"
base_benchfmt="${out_dir}/bench-base-${base_short}-${timestamp}.bench.txt"
head_benchfmt="${out_dir}/bench-head-${head_short}-${timestamp}.bench.txt"
report="${out_dir}/benchstat-${base_short}-to-${head_short}-${timestamp}.txt"

worktree_dir="$(mktemp -d "${TMPDIR:-/tmp}/codemap-bench-base.XXXXXX")"
cleanup() {
  git worktree remove -f "${worktree_dir}" >/dev/null 2>&1 || true
  rm -rf "${worktree_dir}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Preparing base worktree for ${base_ref} (${base_short})"
git worktree add --detach "${worktree_dir}" "${base_commit}" >/dev/null

base_pkg="$(detect_benchmark_package "${worktree_dir}")"
head_pkg="$(detect_benchmark_package "${root}")"

echo "Base benchmark package: ${base_pkg}"
echo "Head benchmark package: ${head_pkg}"

echo "Running base benchmarks: ${base_ref}"
(
  cd "${worktree_dir}"
  go test -run '^$' -bench "${bench_pattern}" -benchmem -count "${bench_count}" -benchtime "${bench_time}" "${base_pkg}"
) | tee "${base_raw}"

grep '^Benchmark' "${base_raw}" > "${base_benchfmt}"
if [[ ! -s "${base_benchfmt}" ]]; then
  echo "error: no benchmark lines found in base run output" >&2
  exit 1
fi

echo "Running head benchmarks: ${head_short}"
go test -run '^$' -bench "${bench_pattern}" -benchmem -count "${bench_count}" -benchtime "${bench_time}" "${head_pkg}" | tee "${head_raw}"

grep '^Benchmark' "${head_raw}" > "${head_benchfmt}"
if [[ ! -s "${head_benchfmt}" ]]; then
  echo "error: no benchmark lines found in head run output" >&2
  exit 1
fi

echo "Computing benchstat diff"
"${benchstat_bin}" "${base_benchfmt}" "${head_benchfmt}" | tee "${report}"

echo "Benchstat report: ${report}"
echo "Base raw output: ${base_raw}"
echo "Head raw output: ${head_raw}"
