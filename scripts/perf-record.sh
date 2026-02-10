#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "${root}"

bench_pattern="${BENCH_PATTERN:-^BenchmarkCodemap}"
bench_time="${BENCH_TIME:-1s}"
bench_count="${BENCH_COUNT:-3}"

mkdir -p perf/history
history_csv="perf/history.csv"

if [[ ! -f "${history_csv}" ]]; then
  echo "timestamp,commit,goos,goarch,goversion,benchmark,run,iterations,ns_per_op,bytes_per_op,allocs_per_op" >"${history_csv}"
fi

ts_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ts_file="$(date -u +%Y%m%dT%H%M%SZ)"
sha="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
goversion="$(go version | awk '{print $3}')"
raw_file="perf/history/bench-${ts_file}-${sha}.txt"

echo "Running benchmarks: pattern=${bench_pattern}, benchtime=${bench_time}, count=${bench_count}"
go test -run '^$' -bench "${bench_pattern}" -benchmem -count "${bench_count}" -benchtime "${bench_time}" ./... | tee "${raw_file}"

awk \
  -v ts="${ts_iso}" \
  -v sha="${sha}" \
  -v goos="${goos}" \
  -v goarch="${goarch}" \
  -v goversion="${goversion}" '
$1 ~ /^BenchmarkCodemap/ {
  bench=$1
  sub(/-[0-9]+$/, "", bench)
  run_idx[bench]++

  iter=$2
  ns=""
  bytes="0"
  allocs="0"

  for (i=1; i<=NF; i++) {
    if ($(i) == "ns/op" && i > 1) {
      ns=$(i-1)
    }
    if ($(i) == "B/op" && i > 1) {
      bytes=$(i-1)
    }
    if ($(i) == "allocs/op" && i > 1) {
      allocs=$(i-1)
    }
  }

  if (iter ~ /^[0-9]+$/ && ns != "") {
    printf "%s,%s,%s,%s,%s,%s,%d,%s,%s,%s,%s\n", ts, sha, goos, goarch, goversion, bench, run_idx[bench], iter, ns, bytes, allocs
  }
}
' "${raw_file}" >> "${history_csv}"

echo "Recorded benchmark history in ${history_csv}"
echo "Saved raw run output in ${raw_file}"
