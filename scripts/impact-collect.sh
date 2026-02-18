#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd -- "${script_dir}/.." && pwd)"

repo_list_file="${IMPACT_REPO_LIST:-${HOME}/.codemap/impact/repos.txt}"
out_dir="${IMPACT_OUT_DIR:-${root}/perf/impact}"
manifest_path="${out_dir}/latest-run.json"
since_override="${IMPACT_SINCE:-}"
since_days="${IMPACT_SINCE_DAYS:-30}"

timestamp_utc="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

date_days_ago() {
  local days="$1"
  if date -u -v-"${days}"d +%Y-%m-%d >/dev/null 2>&1; then
    date -u -v-"${days}"d +%Y-%m-%d
    return 0
  fi
  if date -u -d "-${days} days" +%Y-%m-%d >/dev/null 2>&1; then
    date -u -d "-${days} days" +%Y-%m-%d
    return 0
  fi
  return 1
}

if [[ -n "${since_override}" ]]; then
  since="${since_override}"
else
  since="$(date_days_ago "${since_days}")"
fi

mkdir -p "${out_dir}"

if [[ ! -f "${repo_list_file}" ]]; then
  echo "error: repo list file not found: ${repo_list_file}" >&2
  echo "create it with one absolute repo path per line (comments allowed with #)" >&2
  exit 1
fi

declare -a repos
while IFS= read -r raw_line || [[ -n "${raw_line}" ]]; do
  line="$(echo "${raw_line}" | sed 's/[[:space:]]*$//')"
  [[ -z "${line}" || "${line}" == \#* ]] && continue
  repos+=("${line}")
done <"${repo_list_file}"

if [[ ${#repos[@]} -eq 0 ]]; then
  echo "error: repo list is empty: ${repo_list_file}" >&2
  exit 1
fi

manifest_tmp="${manifest_path}.tmp"
{
  echo "{"
  echo "  \"generated_at_utc\": \"${timestamp_utc}\","
  echo "  \"since\": \"${since}\","
  echo "  \"repo_list_file\": \"${repo_list_file}\","
  echo "  \"output_dir\": \"${out_dir}\","
  echo "  \"repos\": ["
} >"${manifest_tmp}"

ok_count=0
fail_count=0
index=0
for repo in "${repos[@]}"; do
  index=$((index + 1))
  repo_path="${repo/#\~/${HOME}}"
  if [[ "${repo_path}" != /* ]]; then
    repo_path="$(pwd)/${repo_path}"
  fi
  repo_name="$(basename -- "${repo_path}")"
  out_file="${out_dir}/${repo_name}.json"
  tmp_file="${out_file}.tmp"

  status="ok"
  error_msg=""

  if [[ ! -d "${repo_path}" ]]; then
    status="error"
    error_msg="repo path does not exist"
  elif ! python3 "${root}/scripts/impact-report.py" --repo "${repo_path}" --since "${since}" --json >"${tmp_file}" 2>"${tmp_file}.err"; then
    status="error"
    if [[ -s "${tmp_file}.err" ]]; then
      error_msg="$(tr '\n' ' ' <"${tmp_file}.err" | sed 's/"/\\"/g')"
    else
      error_msg="impact-report.py failed"
    fi
  else
    mv "${tmp_file}" "${out_file}"
    ok_count=$((ok_count + 1))
  fi

  rm -f "${tmp_file}" "${tmp_file}.err"

  if [[ "${status}" != "ok" ]]; then
    fail_count=$((fail_count + 1))
    echo "warn: ${repo_path}: ${error_msg}" >&2
  fi

  comma=","
  if [[ "${index}" -eq "${#repos[@]}" ]]; then
    comma=""
  fi

  printf '    {\n' >>"${manifest_tmp}"
  printf '      "repo": "%s",\n' "$(echo "${repo_path}" | sed 's/"/\\"/g')" >>"${manifest_tmp}"
  printf '      "output": "%s",\n' "$(echo "${out_file}" | sed 's/"/\\"/g')" >>"${manifest_tmp}"
  printf '      "status": "%s"' "${status}" >>"${manifest_tmp}"
  if [[ "${status}" != "ok" ]]; then
    printf ',\n      "error": "%s"\n' "${error_msg}" >>"${manifest_tmp}"
  else
    printf '\n' >>"${manifest_tmp}"
  fi
  printf '    }%s\n' "${comma}" >>"${manifest_tmp}"
done

{
  echo "  ],"
  echo "  \"summary\": {"
  echo "    \"repos_total\": ${#repos[@]},"
  echo "    \"repos_succeeded\": ${ok_count},"
  echo "    \"repos_failed\": ${fail_count}"
  echo "  }"
  echo "}"
} >>"${manifest_tmp}"

mv "${manifest_tmp}" "${manifest_path}"

echo "Impact collection complete."
echo "  generated_at_utc: ${timestamp_utc}"
echo "  since: ${since}"
echo "  repos_total: ${#repos[@]}"
echo "  repos_succeeded: ${ok_count}"
echo "  repos_failed: ${fail_count}"
echo "  manifest: ${manifest_path}"
