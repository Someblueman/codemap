#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "${root}"

history_csv="perf/history.csv"
if [[ ! -f "${history_csv}" ]]; then
  echo "No performance history found at ${history_csv}."
  exit 1
fi

tmp_rows="$(mktemp)"
tmp_sorted="$(mktemp)"
tmp_scenarios="$(mktemp)"
trap 'rm -f "${tmp_rows}" "${tmp_sorted}" "${tmp_scenarios}"' EXIT

awk -F, '
NR == 1 { next }
{
  key = $6
  prev[key] = latest[key]
  latest[key] = $0
}
END {
  for (k in latest) {
    split(latest[k], cur, ",")
    latest_ns = cur[9] + 0
    latest_b = cur[10] + 0
    latest_alloc = cur[11] + 0

    prev_ns = ""
    if (prev[k] != "") {
      split(prev[k], old, ",")
      prev_ns = old[9] + 0
    }

    printf "%s,%s,%s,%s,%s\n", k, latest_ns, prev_ns, latest_b, latest_alloc
  }
}
' "${history_csv}" > "${tmp_rows}"

if [[ ! -s "${tmp_rows}" ]]; then
  echo "No benchmark rows found in ${history_csv}."
  exit 1
fi

sort "${tmp_rows}" > "${tmp_sorted}"

format_or_na() {
  local value="$1"
  if [[ -z "${value}" ]]; then
    printf "n/a"
    return
  fi
  printf "%.0f" "${value}"
}

ratio_or_na() {
  local numerator="$1"
  local denominator="$2"
  awk -v n="${numerator}" -v d="${denominator}" 'BEGIN {
    if (n == "" || d == "" || d <= 0) {
      printf "n/a"
      exit 0
    }
    printf "%.2fx", n / d
  }'
}

latest_ns_for_benchmark() {
  local benchmark="$1"
  awk -F, -v b="${benchmark}" '$1 == b { print $2; exit }' "${tmp_sorted}"
}

printf "%-44s %-13s %-13s %-10s %-11s %-11s\n" "Benchmark" "Latest ns/op" "Prev ns/op" "Delta" "Latest B/op" "Allocs/op"
printf "%-44s %-13s %-13s %-10s %-11s %-11s\n" "---------" "------------" "----------" "-----" "----------" "---------"

while IFS=, read -r benchmark latest_ns prev_ns latest_b latest_alloc; do
  if [[ -z "${prev_ns}" ]]; then
    delta="n/a"
    prev_display="n/a"
  else
    delta="$(awk -v cur="${latest_ns}" -v old="${prev_ns}" 'BEGIN {
      if (old <= 0) {
        printf "n/a"
      } else {
        printf "%+.2f%%", ((cur - old) / old) * 100
      }
    }')"
    prev_display="$(printf "%.0f" "${prev_ns}")"
  fi

  printf "%-44s %-13.0f %-13s %-10s %-11.0f %-11.0f\n" \
    "${benchmark}" "${latest_ns}" "${prev_display}" "${delta}" "${latest_b}" "${latest_alloc}"
done < "${tmp_sorted}"

awk -F, '
{
  bench = $1
  if (bench ~ /^BenchmarkCodemapRust/) {
    sub(/^BenchmarkCodemapRust/, "", bench)
    print bench
  } else if (bench ~ /^BenchmarkCodemapTypeScript/) {
    sub(/^BenchmarkCodemapTypeScript/, "", bench)
    print bench
  } else if (bench ~ /^BenchmarkCodemap/) {
    sub(/^BenchmarkCodemap/, "", bench)
    print bench
  }
}
' "${tmp_sorted}" | sort -u > "${tmp_scenarios}"

echo
echo "Language Group Snapshot (Latest Sample)"
printf "%-28s %-12s %-12s %-9s %-12s %-9s\n" "Scenario" "Go ns/op" "Rust ns/op" "Rust/Go" "TS ns/op" "TS/Go"
printf "%-28s %-12s %-12s %-9s %-12s %-9s\n" "--------" "--------" "----------" "-------" "--------" "-----"

while IFS= read -r scenario; do
  [[ -z "${scenario}" ]] && continue

  go_bench="BenchmarkCodemap${scenario}"
  rust_bench="BenchmarkCodemapRust${scenario}"
  ts_bench="BenchmarkCodemapTypeScript${scenario}"

  go_ns="$(latest_ns_for_benchmark "${go_bench}")"
  rust_ns="$(latest_ns_for_benchmark "${rust_bench}")"
  ts_ns="$(latest_ns_for_benchmark "${ts_bench}")"

  rust_ratio="$(ratio_or_na "${rust_ns}" "${go_ns}")"
  ts_ratio="$(ratio_or_na "${ts_ns}" "${go_ns}")"

  printf "%-28s %-12s %-12s %-9s %-12s %-9s\n" \
    "${scenario}" \
    "$(format_or_na "${go_ns}")" \
    "$(format_or_na "${rust_ns}")" \
    "${rust_ratio}" \
    "$(format_or_na "${ts_ns}")" \
    "${ts_ratio}"
done < "${tmp_scenarios}"
