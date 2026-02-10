#!/usr/bin/env python3
"""
Generate codemap impact metrics from local Codex/Claude session logs.

This script is designed to be repo-agnostic: point it at any git repository.
It reports:
- Codemap adoption (touch rate and early-touch rate)
- Speed-to-first-edit proxy (actions before first edit)
- File-open proxy (unique Read paths before first edit; Claude sessions only)
- Optional success metrics (if labeled outcomes CSV is provided)
"""

from __future__ import annotations

import argparse
import csv
import dataclasses
import datetime as dt
import json
import re
import statistics
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Set, Tuple

CODEMAP_REF_RE = re.compile(r"CODEMAP\.(?:paths|md)")
STRICT_CODEMAP_RUN_RE = re.compile(r"(^|[;&|]\s*|\n)\s*codemap(\s|$)")
GIT_COMMIT_RE = re.compile(r"\bgit\s+commit\b")
APPLY_PATCH_IN_BASH_RE = re.compile(r"\bapply_patch\b|\bgit\s+apply\b")
EDIT_TOOL_NAMES = {"Edit", "Write", "MultiEdit"}
CODEX_COMMAND_TOOL_NAMES = {"exec_command", "shell_command"}


@dataclasses.dataclass
class SessionStats:
    source: str
    session_id: str
    session_file: str
    first_date: Optional[str] = None
    action_count: int = 0
    first_codemap_action_index: Optional[int] = None
    first_edit_action_index: Optional[int] = None
    unique_reads_before_edit: Set[str] = dataclasses.field(default_factory=set)
    has_explicit_codemap_run: bool = False
    has_commit_command: bool = False

    def record_action(
        self,
        *,
        date_str: Optional[str],
        codemap_event: bool = False,
        edit_event: bool = False,
        read_path: Optional[str] = None,
    ) -> None:
        self.action_count += 1
        if self.first_date is None and date_str:
            self.first_date = date_str
        if codemap_event and self.first_codemap_action_index is None:
            self.first_codemap_action_index = self.action_count
        if read_path and self.first_edit_action_index is None:
            self.unique_reads_before_edit.add(read_path)
        if edit_event and self.first_edit_action_index is None:
            self.first_edit_action_index = self.action_count

    @property
    def touched_codemap(self) -> bool:
        return self.first_codemap_action_index is not None

    @property
    def touched_codemap_early(self) -> bool:
        idx = self.first_codemap_action_index
        return idx is not None and idx <= 3

    @property
    def actions_before_first_edit(self) -> Optional[int]:
        if self.first_edit_action_index is None:
            return None
        return self.first_edit_action_index - 1


@dataclasses.dataclass
class AggregateMetrics:
    sessions_total: int
    sessions_touching_codemap: int
    sessions_touching_codemap_early: int
    sessions_with_edit: int
    sessions_with_explicit_codemap_run: int
    sessions_with_commit_command: int
    median_actions_before_first_edit: Optional[float]
    median_actions_before_first_edit_early_codemap: Optional[float]
    median_actions_before_first_edit_no_early_codemap: Optional[float]
    median_unique_reads_before_first_edit: Optional[float]
    median_unique_reads_before_first_edit_early_codemap: Optional[float]
    median_unique_reads_before_first_edit_no_early_codemap: Optional[float]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate codemap impact metrics from local logs.")
    parser.add_argument(
        "--repo",
        required=True,
        help="Path to the target repository (absolute or relative).",
    )
    parser.add_argument(
        "--since",
        help="Start date (inclusive), YYYY-MM-DD. Default: no lower bound.",
    )
    parser.add_argument(
        "--until",
        help="End date (inclusive), YYYY-MM-DD. Default: no upper bound.",
    )
    parser.add_argument(
        "--codex-sessions-root",
        default=str(Path.home() / ".codex" / "sessions"),
        help="Codex sessions root. Default: ~/.codex/sessions",
    )
    parser.add_argument(
        "--claude-projects-root",
        default=str(Path.home() / ".claude" / "projects"),
        help="Claude projects root. Default: ~/.claude/projects",
    )
    parser.add_argument(
        "--outcomes-csv",
        help=(
            "Optional labeled outcomes CSV with columns: source,session_id,success. "
            "Success must be one of: 1,true,yes,0,false,no."
        ),
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Emit machine-readable JSON output.",
    )
    return parser.parse_args()


def parse_date_input(value: Optional[str], flag: str) -> Optional[dt.date]:
    if not value:
        return None
    try:
        return dt.date.fromisoformat(value)
    except ValueError as exc:
        raise SystemExit(f"invalid {flag} value '{value}': expected YYYY-MM-DD") from exc


def timestamp_to_date_str(value: Any) -> Optional[str]:
    if not isinstance(value, str) or len(value) < 10:
        return None
    maybe = value[:10]
    try:
        dt.date.fromisoformat(maybe)
        return maybe
    except ValueError:
        return None


def in_window(date_str: Optional[str], since: Optional[dt.date], until: Optional[dt.date]) -> bool:
    if date_str is None:
        return False
    try:
        date_value = dt.date.fromisoformat(date_str)
    except ValueError:
        return False
    if since and date_value < since:
        return False
    if until and date_value > until:
        return False
    return True


def run_git_command(repo_path: Path, args: List[str]) -> Optional[str]:
    try:
        proc = subprocess.run(
            ["git", "-C", str(repo_path), *args],
            check=True,
            capture_output=True,
            text=True,
        )
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None
    out = proc.stdout.strip()
    return out if out else None


def repo_origin(repo_root: Path) -> Optional[str]:
    return run_git_command(repo_root, ["remote", "get-url", "origin"])


def parse_json_line(raw: str) -> Optional[Dict[str, Any]]:
    raw = raw.strip()
    if not raw:
        return None
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        return None
    if isinstance(data, dict):
        return data
    return None


def codex_session_matches_repo(
    cwd: Any,
    *,
    repo_root: Path,
    repo_origin_url: Optional[str],
    cwd_origin_cache: Dict[str, Optional[str]],
) -> bool:
    if not isinstance(cwd, str) or not cwd:
        return False
    cwd_path = Path(cwd)
    try:
        resolved = cwd_path.resolve()
    except OSError:
        resolved = cwd_path

    if resolved == repo_root or repo_root in resolved.parents:
        return True

    if not repo_origin_url:
        return False

    cache_key = str(resolved)
    if cache_key not in cwd_origin_cache:
        cwd_origin_cache[cache_key] = run_git_command(resolved, ["remote", "get-url", "origin"])
    return cwd_origin_cache[cache_key] == repo_origin_url


def parse_codex_command(payload: Dict[str, Any]) -> Optional[str]:
    name = payload.get("name")
    if name not in CODEX_COMMAND_TOOL_NAMES:
        return None
    args_raw = payload.get("arguments")
    args_obj: Dict[str, Any] = {}
    if isinstance(args_raw, str):
        try:
            parsed = json.loads(args_raw)
            if isinstance(parsed, dict):
                args_obj = parsed
        except json.JSONDecodeError:
            return None
    elif isinstance(args_raw, dict):
        args_obj = args_raw
    if name == "exec_command":
        cmd = args_obj.get("cmd")
    else:
        cmd = args_obj.get("command")
    if isinstance(cmd, str) and cmd.strip():
        return cmd
    return None


def collect_codex_sessions(
    *,
    repo_root: Path,
    since: Optional[dt.date],
    until: Optional[dt.date],
    codex_sessions_root: Path,
) -> List[SessionStats]:
    if not codex_sessions_root.exists():
        return []

    repo_root_str = str(repo_root)
    repo_origin_url = repo_origin(repo_root)
    cwd_origin_cache: Dict[str, Optional[str]] = {}

    sessions: List[SessionStats] = []
    for file_path in codex_sessions_root.rglob("*.jsonl"):
        include_session = False
        session_id = file_path.stem
        parsed_events: List[Tuple[str, str, Optional[str]]] = []

        try:
            raw_lines = file_path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            continue

        for raw in raw_lines:
            if repo_root_str in raw:
                include_session = True
            obj = parse_json_line(raw)
            if obj is None:
                continue

            date_str = timestamp_to_date_str(obj.get("timestamp"))
            event_type = obj.get("type")

            if event_type == "session_meta":
                payload = obj.get("payload")
                if isinstance(payload, dict):
                    maybe_session_id = payload.get("id")
                    if isinstance(maybe_session_id, str) and maybe_session_id:
                        session_id = maybe_session_id
                    if codex_session_matches_repo(
                        payload.get("cwd"),
                        repo_root=repo_root,
                        repo_origin_url=repo_origin_url,
                        cwd_origin_cache=cwd_origin_cache,
                    ):
                        include_session = True
                continue

            if event_type != "response_item":
                continue

            payload = obj.get("payload")
            if not isinstance(payload, dict):
                continue

            payload_type = payload.get("type")
            if payload_type == "function_call":
                command = parse_codex_command(payload)
                if command:
                    parsed_events.append(("command", command, date_str))
                elif payload.get("name") == "apply_patch":
                    parsed_events.append(("edit", "apply_patch", date_str))
            elif payload_type == "custom_tool_call" and payload.get("name") == "apply_patch":
                parsed_events.append(("edit", "apply_patch", date_str))

        if not include_session:
            continue

        stats = SessionStats(
            source="codex",
            session_id=session_id,
            session_file=str(file_path),
        )
        for kind, value, date_str in parsed_events:
            if not in_window(date_str, since, until):
                continue
            if kind == "command":
                codemap_event = bool(CODEMAP_REF_RE.search(value))
                stats.record_action(date_str=date_str, codemap_event=codemap_event)
                if STRICT_CODEMAP_RUN_RE.search(value):
                    stats.has_explicit_codemap_run = True
                if GIT_COMMIT_RE.search(value):
                    stats.has_commit_command = True
            elif kind == "edit":
                stats.record_action(date_str=date_str, edit_event=True)

        if stats.action_count > 0:
            sessions.append(stats)

    return sessions


def repo_to_claude_project_dir(repo_root: Path, claude_projects_root: Path) -> Path:
    parts = [part for part in repo_root.resolve().parts if part not in ("/", "")]
    key = "-" + "-".join(parts)
    return claude_projects_root / key


def extract_tool_uses(obj: Dict[str, Any]) -> Iterable[Dict[str, Any]]:
    buckets: List[Any] = []

    message = obj.get("message")
    if isinstance(message, dict):
        content = message.get("content")
        if isinstance(content, list):
            buckets.extend(content)

    data = obj.get("data")
    if isinstance(data, dict):
        data_message = data.get("message")
        if isinstance(data_message, dict):
            inner_message = data_message.get("message")
            if isinstance(inner_message, dict):
                inner_content = inner_message.get("content")
                if isinstance(inner_content, list):
                    buckets.extend(inner_content)

    for item in buckets:
        if isinstance(item, dict) and item.get("type") == "tool_use":
            yield item


def collect_claude_sessions(
    *,
    repo_root: Path,
    since: Optional[dt.date],
    until: Optional[dt.date],
    claude_projects_root: Path,
) -> List[SessionStats]:
    project_dir = repo_to_claude_project_dir(repo_root, claude_projects_root)
    if not project_dir.exists():
        return []

    sessions: List[SessionStats] = []
    for file_path in project_dir.glob("*.jsonl"):
        stats = SessionStats(
            source="claude",
            session_id=file_path.stem,
            session_file=str(file_path),
        )
        seen_tool_ids: Set[str] = set()

        try:
            raw_lines = file_path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            continue

        for raw in raw_lines:
            obj = parse_json_line(raw)
            if obj is None:
                continue
            date_str = timestamp_to_date_str(obj.get("timestamp"))

            for tool in extract_tool_uses(obj):
                tool_id = tool.get("id")
                if isinstance(tool_id, str) and tool_id:
                    if tool_id in seen_tool_ids:
                        continue
                    seen_tool_ids.add(tool_id)

                name = tool.get("name")
                tool_input = tool.get("input")
                if not isinstance(tool_input, dict):
                    tool_input = {}

                if name == "Bash":
                    command = tool_input.get("command")
                    if not isinstance(command, str) or not command:
                        continue
                    if not in_window(date_str, since, until):
                        continue
                    codemap_event = bool(CODEMAP_REF_RE.search(command))
                    edit_event = bool(APPLY_PATCH_IN_BASH_RE.search(command))
                    stats.record_action(
                        date_str=date_str,
                        codemap_event=codemap_event,
                        edit_event=edit_event,
                    )
                    if STRICT_CODEMAP_RUN_RE.search(command):
                        stats.has_explicit_codemap_run = True
                    if GIT_COMMIT_RE.search(command):
                        stats.has_commit_command = True

                elif name == "Read":
                    file_ref = tool_input.get("file_path")
                    if not isinstance(file_ref, str) or not file_ref:
                        continue
                    if not in_window(date_str, since, until):
                        continue
                    codemap_event = bool(CODEMAP_REF_RE.search(file_ref))
                    stats.record_action(
                        date_str=date_str,
                        codemap_event=codemap_event,
                        read_path=file_ref,
                    )

                elif name in EDIT_TOOL_NAMES:
                    if not in_window(date_str, since, until):
                        continue
                    stats.record_action(date_str=date_str, edit_event=True)

        if stats.action_count > 0:
            sessions.append(stats)

    return sessions


def safe_median(values: List[int]) -> Optional[float]:
    if not values:
        return None
    return float(statistics.median(values))


def compute_aggregate(sessions: List[SessionStats], include_read_metric: bool) -> AggregateMetrics:
    sessions_total = len(sessions)
    touching = [s for s in sessions if s.touched_codemap]
    early_touching = [s for s in sessions if s.touched_codemap_early]
    edited = [s for s in sessions if s.first_edit_action_index is not None]
    explicit_runs = [s for s in sessions if s.has_explicit_codemap_run]
    commits = [s for s in sessions if s.has_commit_command]

    before_edit_all = [s.actions_before_first_edit for s in edited if s.actions_before_first_edit is not None]
    before_edit_early = [
        s.actions_before_first_edit
        for s in edited
        if s.actions_before_first_edit is not None and s.touched_codemap_early
    ]
    before_edit_no_early = [
        s.actions_before_first_edit
        for s in edited
        if s.actions_before_first_edit is not None and not s.touched_codemap_early
    ]

    if include_read_metric:
        reads_all = [len(s.unique_reads_before_edit) for s in edited]
        reads_early = [len(s.unique_reads_before_edit) for s in edited if s.touched_codemap_early]
        reads_no_early = [len(s.unique_reads_before_edit) for s in edited if not s.touched_codemap_early]
        median_reads_all = safe_median(reads_all)
        median_reads_early = safe_median(reads_early)
        median_reads_no_early = safe_median(reads_no_early)
    else:
        median_reads_all = None
        median_reads_early = None
        median_reads_no_early = None

    return AggregateMetrics(
        sessions_total=sessions_total,
        sessions_touching_codemap=len(touching),
        sessions_touching_codemap_early=len(early_touching),
        sessions_with_edit=len(edited),
        sessions_with_explicit_codemap_run=len(explicit_runs),
        sessions_with_commit_command=len(commits),
        median_actions_before_first_edit=safe_median(before_edit_all),
        median_actions_before_first_edit_early_codemap=safe_median(before_edit_early),
        median_actions_before_first_edit_no_early_codemap=safe_median(before_edit_no_early),
        median_unique_reads_before_first_edit=median_reads_all,
        median_unique_reads_before_first_edit_early_codemap=median_reads_early,
        median_unique_reads_before_first_edit_no_early_codemap=median_reads_no_early,
    )


def pct(num: int, den: int) -> Optional[float]:
    if den == 0:
        return None
    return (num * 100.0) / den


def fmt_num(value: Optional[float]) -> str:
    if value is None:
        return "n/a"
    if value.is_integer():
        return str(int(value))
    return f"{value:.1f}"


def fmt_pct(value: Optional[float]) -> str:
    if value is None:
        return "n/a"
    return f"{value:.1f}%"


def load_outcomes_csv(path: Path) -> Dict[Tuple[str, str], bool]:
    if not path.exists():
        raise SystemExit(f"outcomes CSV not found: {path}")

    results: Dict[Tuple[str, str], bool] = {}
    with path.open("r", encoding="utf-8", newline="") as f:
        reader = csv.DictReader(f)
        required = {"source", "session_id", "success"}
        if reader.fieldnames is None or not required.issubset(set(reader.fieldnames)):
            raise SystemExit(
                "outcomes CSV must include header columns: source,session_id,success"
            )
        for row in reader:
            source = (row.get("source") or "").strip().lower()
            session_id = (row.get("session_id") or "").strip()
            success_raw = (row.get("success") or "").strip().lower()
            if not source or not session_id:
                continue
            if success_raw in {"1", "true", "yes"}:
                success = True
            elif success_raw in {"0", "false", "no"}:
                success = False
            else:
                continue
            results[(source, session_id)] = success
    return results


def match_labeled_success(
    sessions: List[SessionStats],
    labels: Dict[Tuple[str, str], bool],
) -> Dict[str, Any]:
    matched: List[Tuple[SessionStats, bool]] = []
    for session in sessions:
        candidates = [
            session.session_id,
            Path(session.session_file).stem,
            session.session_file,
        ]
        label: Optional[bool] = None
        for candidate in candidates:
            key = (session.source, candidate)
            if key in labels:
                label = labels[key]
                break
        if label is None:
            continue
        matched.append((session, label))

    codemap_yes = [success for session, success in matched if session.touched_codemap]
    codemap_no = [success for session, success in matched if not session.touched_codemap]

    def success_rate(values: List[bool]) -> Optional[float]:
        if not values:
            return None
        return (sum(1 for v in values if v) * 100.0) / len(values)

    return {
        "labeled_sessions": len(matched),
        "codemap_sessions": len(codemap_yes),
        "non_codemap_sessions": len(codemap_no),
        "success_rate_codemap": success_rate(codemap_yes),
        "success_rate_non_codemap": success_rate(codemap_no),
    }


def aggregate_to_dict(aggregate: AggregateMetrics) -> Dict[str, Any]:
    return {
        "sessions_total": aggregate.sessions_total,
        "sessions_touching_codemap": aggregate.sessions_touching_codemap,
        "sessions_touching_codemap_pct": pct(
            aggregate.sessions_touching_codemap, aggregate.sessions_total
        ),
        "sessions_touching_codemap_early": aggregate.sessions_touching_codemap_early,
        "sessions_touching_codemap_early_pct_of_touching": pct(
            aggregate.sessions_touching_codemap_early, aggregate.sessions_touching_codemap
        ),
        "sessions_with_edit": aggregate.sessions_with_edit,
        "sessions_with_explicit_codemap_run": aggregate.sessions_with_explicit_codemap_run,
        "sessions_with_commit_command": aggregate.sessions_with_commit_command,
        "median_actions_before_first_edit": aggregate.median_actions_before_first_edit,
        "median_actions_before_first_edit_early_codemap": (
            aggregate.median_actions_before_first_edit_early_codemap
        ),
        "median_actions_before_first_edit_no_early_codemap": (
            aggregate.median_actions_before_first_edit_no_early_codemap
        ),
        "median_unique_reads_before_first_edit": (
            aggregate.median_unique_reads_before_first_edit
        ),
        "median_unique_reads_before_first_edit_early_codemap": (
            aggregate.median_unique_reads_before_first_edit_early_codemap
        ),
        "median_unique_reads_before_first_edit_no_early_codemap": (
            aggregate.median_unique_reads_before_first_edit_no_early_codemap
        ),
    }


def print_source_block(title: str, aggregate: AggregateMetrics, has_read_metric: bool) -> None:
    print(f"{title}:")
    print(f"  sessions analyzed: {aggregate.sessions_total}")
    print(
        "  codemap touch rate: "
        f"{aggregate.sessions_touching_codemap}/{aggregate.sessions_total} "
        f"({fmt_pct(pct(aggregate.sessions_touching_codemap, aggregate.sessions_total))})"
    )
    print(
        "  early codemap touch (<=3 actions): "
        f"{aggregate.sessions_touching_codemap_early}/{aggregate.sessions_touching_codemap} "
        f"({fmt_pct(pct(aggregate.sessions_touching_codemap_early, aggregate.sessions_touching_codemap))})"
    )
    print(f"  sessions with edits: {aggregate.sessions_with_edit}")
    print(
        "  median actions before first edit: "
        f"all={fmt_num(aggregate.median_actions_before_first_edit)}, "
        f"early_codemap={fmt_num(aggregate.median_actions_before_first_edit_early_codemap)}, "
        f"no_early_codemap={fmt_num(aggregate.median_actions_before_first_edit_no_early_codemap)}"
    )
    if has_read_metric:
        print(
            "  median unique reads before first edit: "
            f"all={fmt_num(aggregate.median_unique_reads_before_first_edit)}, "
            f"early_codemap={fmt_num(aggregate.median_unique_reads_before_first_edit_early_codemap)}, "
            f"no_early_codemap={fmt_num(aggregate.median_unique_reads_before_first_edit_no_early_codemap)}"
        )
    else:
        print("  median unique reads before first edit: n/a (no direct Read-tool events)")
    print(
        "  explicit `codemap` command sessions: "
        f"{aggregate.sessions_with_explicit_codemap_run}"
    )
    print(f"  sessions with `git commit`: {aggregate.sessions_with_commit_command}")


def main() -> int:
    args = parse_args()
    repo_root = Path(args.repo).expanduser().resolve()
    if not repo_root.exists():
        raise SystemExit(f"repo path does not exist: {repo_root}")

    since = parse_date_input(args.since, "--since")
    until = parse_date_input(args.until, "--until")
    if since and until and since > until:
        raise SystemExit("--since must be <= --until")

    codex_root = Path(args.codex_sessions_root).expanduser()
    claude_root = Path(args.claude_projects_root).expanduser()

    codex_sessions = collect_codex_sessions(
        repo_root=repo_root,
        since=since,
        until=until,
        codex_sessions_root=codex_root,
    )
    claude_sessions = collect_claude_sessions(
        repo_root=repo_root,
        since=since,
        until=until,
        claude_projects_root=claude_root,
    )
    combined_sessions = codex_sessions + claude_sessions

    codex_agg = compute_aggregate(codex_sessions, include_read_metric=False)
    claude_agg = compute_aggregate(claude_sessions, include_read_metric=True)
    combined_agg = compute_aggregate(combined_sessions, include_read_metric=False)

    success_summary: Optional[Dict[str, Any]] = None
    if args.outcomes_csv:
        labels = load_outcomes_csv(Path(args.outcomes_csv).expanduser())
        success_summary = match_labeled_success(combined_sessions, labels)

    output = {
        "repo": str(repo_root),
        "window": {
            "since": since.isoformat() if since else None,
            "until": until.isoformat() if until else None,
        },
        "sources": {
            "codex_sessions_root": str(codex_root),
            "claude_projects_root": str(claude_root),
            "claude_project_dir": str(repo_to_claude_project_dir(repo_root, claude_root)),
        },
        "metrics": {
            "codex": aggregate_to_dict(codex_agg),
            "claude": aggregate_to_dict(claude_agg),
            "combined": aggregate_to_dict(combined_agg),
        },
        "success_labels": success_summary,
    }

    if args.json:
        json.dump(output, sys.stdout, indent=2)
        print()
        return 0

    print("Codemap Impact Report")
    print(f"Repo: {repo_root}")
    print(
        "Window: "
        f"{since.isoformat() if since else 'unbounded'} -> {until.isoformat() if until else 'unbounded'}"
    )
    print(f"Codex sessions root: {codex_root}")
    print(f"Claude project dir: {repo_to_claude_project_dir(repo_root, claude_root)}")
    print()
    print_source_block("Codex", codex_agg, has_read_metric=False)
    print()
    print_source_block("Claude", claude_agg, has_read_metric=True)
    print()
    print_source_block("Combined", combined_agg, has_read_metric=False)
    print()
    if success_summary is None:
        print(
            "Success metric: n/a (provide --outcomes-csv with columns "
            "source,session_id,success)"
        )
    else:
        print("Success metric (labeled sessions only):")
        print(f"  labeled sessions matched: {success_summary['labeled_sessions']}")
        print(
            "  success rate (codemap sessions): "
            f"{fmt_pct(success_summary['success_rate_codemap'])} "
            f"over {success_summary['codemap_sessions']} sessions"
        )
        print(
            "  success rate (non-codemap sessions): "
            f"{fmt_pct(success_summary['success_rate_non_codemap'])} "
            f"over {success_summary['non_codemap_sessions']} sessions"
        )

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
