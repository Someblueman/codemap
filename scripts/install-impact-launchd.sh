#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/install-impact-launchd.sh [--hour N] [--minute N] [--since-days N] [--run-now]
  ./scripts/install-impact-launchd.sh --uninstall

Options:
  --hour N        Local hour to run (0-23). Default: 9
  --minute N      Local minute to run (0-59). Default: 0
  --since-days N  Passed to IMPACT_SINCE_DAYS. Default: 30
  --run-now       Trigger a run immediately after install.
  --uninstall     Remove the launch agent.
EOF
}

hour=9
minute=0
since_days=30
run_now=0
uninstall=0

while [[ $# -gt 0 ]]; do
  case "$1" in
  --hour)
    hour="${2:-}"
    shift 2
    ;;
  --minute)
    minute="${2:-}"
    shift 2
    ;;
  --since-days)
    since_days="${2:-}"
    shift 2
    ;;
  --run-now)
    run_now=1
    shift
    ;;
  --uninstall)
    uninstall=1
    shift
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "error: unknown option: $1" >&2
    usage >&2
    exit 1
    ;;
  esac
done

if [[ ! "${hour}" =~ ^[0-9]+$ ]] || [[ "${hour}" -lt 0 || "${hour}" -gt 23 ]]; then
  echo "error: --hour must be 0..23" >&2
  exit 1
fi
if [[ ! "${minute}" =~ ^[0-9]+$ ]] || [[ "${minute}" -lt 0 || "${minute}" -gt 59 ]]; then
  echo "error: --minute must be 0..59" >&2
  exit 1
fi
if [[ ! "${since_days}" =~ ^[0-9]+$ ]] || [[ "${since_days}" -lt 1 ]]; then
  echo "error: --since-days must be >= 1" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
collector_script="${repo_root}/scripts/impact-collect.sh"
label="com.someblueman.codemap.impact-collect"
uid="$(id -u)"
launch_agents_dir="${HOME}/Library/LaunchAgents"
plist_path="${launch_agents_dir}/${label}.plist"
impact_dir="${repo_root}/perf/impact"
repo_list_dir="${HOME}/.codemap/impact"
repo_list_file="${repo_list_dir}/repos.txt"

mkdir -p "${impact_dir}"
mkdir -p "${repo_list_dir}"
mkdir -p "${launch_agents_dir}"

if [[ ! -f "${repo_list_file}" ]]; then
  {
    echo "# One absolute repo path per line"
    echo "${repo_root}"
    if [[ -d "${HOME}/Code/freebird" ]]; then
      echo "${HOME}/Code/freebird"
    fi
  } >"${repo_list_file}"
  echo "Created ${repo_list_file}"
fi

if [[ "${uninstall}" -eq 1 ]]; then
  launchctl bootout "gui/${uid}" "${plist_path}" >/dev/null 2>&1 || true
  rm -f "${plist_path}"
  echo "Removed launch agent: ${label}"
  exit 0
fi

cat >"${plist_path}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${collector_script}</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${repo_root}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>IMPACT_REPO_LIST</key>
    <string>${repo_list_file}</string>
    <key>IMPACT_OUT_DIR</key>
    <string>${impact_dir}</string>
    <key>IMPACT_SINCE_DAYS</key>
    <string>${since_days}</string>
  </dict>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key>
    <integer>${hour}</integer>
    <key>Minute</key>
    <integer>${minute}</integer>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${impact_dir}/launchd.stdout.log</string>
  <key>StandardErrorPath</key>
  <string>${impact_dir}/launchd.stderr.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/${uid}" "${plist_path}" >/dev/null 2>&1 || true
launchctl bootstrap "gui/${uid}" "${plist_path}"
launchctl enable "gui/${uid}/${label}"

if [[ "${run_now}" -eq 1 ]]; then
  launchctl kickstart -k "gui/${uid}/${label}"
fi

echo "Installed launch agent: ${label}"
echo "  schedule (local): ${hour}:$(printf '%02d' "${minute}") daily"
echo "  plist: ${plist_path}"
echo "  repo list: ${repo_list_file}"
echo "  output dir: ${impact_dir}"
if [[ "${run_now}" -eq 1 ]]; then
  echo "  triggered: yes"
fi
echo "Manage:"
echo "  launchctl print gui/${uid}/${label}"
echo "  ./scripts/install-impact-launchd.sh --uninstall"
