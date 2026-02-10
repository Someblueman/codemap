#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

target_root="${1:-}"
if [[ -z "${target_root}" ]]; then
  if ! target_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
    echo "error: run from inside a git repo or pass the repo path" >&2
    exit 1
  fi
fi

if [[ ! -d "${target_root}/.git" ]]; then
  echo "error: ${target_root} is not a git repository" >&2
  exit 1
fi

if ! command -v codemap >/dev/null 2>&1; then
  echo "error: codemap not found in PATH; install it first, then re-run this script" >&2
  echo "hint: go install github.com/Someblueman/codemap@latest" >&2
  exit 1
fi

hooks_dir="${target_root}/.git/hooks"
hook_path="${hooks_dir}/pre-commit"
template_path="${script_dir}/pre-commit"

mkdir -p "${hooks_dir}"

if [[ -f "${hook_path}" ]]; then
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  backup="${hook_path}.bak.${ts}"
  cp "${hook_path}" "${backup}"
  echo "Backed up existing pre-commit hook to: ${backup}"
fi

cp "${template_path}" "${hook_path}"
chmod +x "${hook_path}"
echo "Installed pre-commit hook: ${hook_path}"

(cd "${target_root}" && codemap)
(cd "${target_root}" && git add CODEMAP.md CODEMAP.paths 2>/dev/null || true)

ensure_codemap_block() {
  local file_path="$1"
  local begin="<!-- codemap:begin -->"

  if [[ -f "${file_path}" ]] && grep -Fq "${begin}" "${file_path}"; then
    return 0
  fi

  if [[ ! -f "${file_path}" ]]; then
    cat >"${file_path}" <<'EOF'
# Agent Instructions
EOF
  fi

  cat >>"${file_path}" <<'EOF'

<!-- codemap:begin -->
## Codemap Routing (Agents)

Always start by reading `CODEMAP.paths` to find the smallest set of files to inspect/edit. Avoid reading large architecture/docs files unless needed.
<!-- codemap:end -->
EOF
}

ensure_codemap_block "${target_root}/AGENTS.md"
ensure_codemap_block "${target_root}/CLAUDE.md"

(cd "${target_root}" && git add AGENTS.md CLAUDE.md 2>/dev/null || true)

echo "Updated AGENTS.md/CLAUDE.md with CODEMAP.paths guidance (idempotent block)."

ensure_gitignore_entry() {
  local file_path="$1"
  local entry="$2"

  if [[ ! -f "${file_path}" ]]; then
    touch "${file_path}"
  fi

  if ! grep -Fxq "${entry}" "${file_path}"; then
    {
      echo ""
      echo "# codemap local cache"
      echo "${entry}"
    } >>"${file_path}"
  fi
}

ensure_gitignore_entry "${target_root}/.gitignore" ".codemap.state.json"

(cd "${target_root}" && git add .gitignore 2>/dev/null || true)

echo "Updated .gitignore with .codemap.state.json ignore rule."
