#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "${root}"

history_csv="perf/history.csv"
if [[ ! -f "${history_csv}" ]]; then
  echo "No performance history found at ${history_csv}."
  exit 1
fi

awk -F, '
NR == 1 { next }
{
  key = $6
  prev[key] = latest[key]
  latest[key] = $0
}
END {
  printf "%-36s %-13s %-13s %-10s\n", "Benchmark", "Latest ns/op", "Prev ns/op", "Delta"
  printf "%-36s %-13s %-13s %-10s\n", "---------", "------------", "----------", "-----"

  for (k in latest) {
    split(latest[k], cur, ",")
    latest_ns = cur[9] + 0

    if (prev[k] == "") {
      printf "%-36s %-13.0f %-13s %-10s\n", k, latest_ns, "n/a", "n/a"
      continue
    }

    split(prev[k], old, ",")
    prev_ns = old[9] + 0
    if (prev_ns <= 0) {
      printf "%-36s %-13.0f %-13s %-10s\n", k, latest_ns, "n/a", "n/a"
      continue
    }

    delta = ((latest_ns - prev_ns) / prev_ns) * 100
    printf "%-36s %-13.0f %-13.0f %+.2f%%\n", k, latest_ns, prev_ns, delta
  }
}
' "${history_csv}"
