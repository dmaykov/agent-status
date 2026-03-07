#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import sys
import time
from collections import defaultdict
from datetime import datetime
from pathlib import Path
from typing import Any


HOME = Path(os.environ.get("HOME", str(Path.home())))
CACHE_ROOT = Path(os.environ.get("XDG_CACHE_HOME", str(HOME / ".cache"))) / "agent-status"
STATE_FILE = CACHE_ROOT / "state.json"
SESSIONS_DIR = CACHE_ROOT / "sessions"
CODEX_STATE_DB = HOME / ".codex" / "state_5.sqlite"
CLAUDE_PROJECTS_DIR = HOME / ".claude" / "projects"
AGENT_NAMES = {"codex", "claude"}
CLAUDE_SESSION_CACHE: dict[str, dict[str, Any]] = {}

try:
    CLK_TCK = int(os.sysconf("SC_CLK_TCK"))
except (ValueError, OSError):
    CLK_TCK = 100


def debug(message: str) -> None:
    print(message, file=sys.stderr, flush=True)


def ensure_dirs() -> None:
    CACHE_ROOT.mkdir(parents=True, exist_ok=True)
    SESSIONS_DIR.mkdir(parents=True, exist_ok=True)


def read_boot_time() -> float:
    try:
        for line in Path("/proc/stat").read_text().splitlines():
            if line.startswith("btime "):
                return float(line.split()[1])
    except OSError:
        pass

    return time.time()


BOOT_TIME = read_boot_time()


def run_json(command: list[str]) -> Any:
    try:
        completed = subprocess.run(command, check=True, capture_output=True, text=True)
    except subprocess.CalledProcessError as error:
        debug(f"command failed: {' '.join(command)}: {error.stderr.strip()}")
        return []

    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError:
        debug(f"invalid json from {' '.join(command)}")
        return []


def read_proc_table() -> tuple[dict[int, dict[str, Any]], dict[int, list[int]]]:
    processes: dict[int, dict[str, Any]] = {}
    children: dict[int, list[int]] = defaultdict(list)

    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue

        pid = int(entry.name)
        stat_path = entry / "stat"
        cmdline_path = entry / "cmdline"
        cwd_path = entry / "cwd"

        try:
            stat_text = stat_path.read_text()
        except OSError:
            continue

        rparen = stat_text.rfind(")")
        if rparen == -1:
            continue

        fields = stat_text[rparen + 2 :].split()
        if len(fields) < 2:
            continue

        try:
            ppid = int(fields[1])
            start_ticks = int(fields[19]) if len(fields) > 19 else 0
        except ValueError:
            continue

        try:
            cmdline = cmdline_path.read_bytes().decode("utf-8", errors="ignore").split("\0")
        except OSError:
            cmdline = []

        cmdline = [part for part in cmdline if part]

        try:
            cwd = os.readlink(cwd_path)
        except OSError:
            cwd = ""

        exe_name = Path(cmdline[0]).name if cmdline else ""
        processes[pid] = {
            "pid": pid,
            "ppid": ppid,
            "start_ticks": start_ticks,
            "cmdline": cmdline,
            "exe_name": exe_name,
            "cmd_text": " ".join(cmdline),
            "cwd": cwd,
        }
        children[ppid].append(pid)

    return processes, children


def walk_descendants(pid: int, children: dict[int, list[int]]) -> list[int]:
    queue = [pid]
    seen: set[int] = set()
    ordered: list[int] = []

    while queue:
        current = queue.pop(0)
        if current in seen:
            continue
        seen.add(current)
        ordered.append(current)
        queue.extend(children.get(current, []))

    return ordered


def parse_session_file(pid: int) -> dict[str, str]:
    session_path = SESSIONS_DIR / f"{pid}.session"
    if not session_path.exists():
        return {}

    data: dict[str, str] = {}
    try:
        for line in session_path.read_text().splitlines():
            if "\t" not in line:
                continue
            key, value = line.split("\t", 1)
            data[key] = value
    except OSError:
        return {}

    return data


def cleanup_sessions() -> None:
    for session_file in SESSIONS_DIR.glob("*.session"):
        try:
            pid = int(session_file.stem)
        except ValueError:
            continue
        if not Path(f"/proc/{pid}").exists():
            try:
                session_file.unlink()
            except OSError:
                pass


def process_start_epoch(proc: dict[str, Any]) -> float:
    start_ticks = int(proc.get("start_ticks") or 0)
    if start_ticks <= 0:
        return 0.0
    return BOOT_TIME + (start_ticks / CLK_TCK)


def first_nonempty_line(text: str) -> str:
    for line in text.splitlines():
        stripped = line.strip()
        if stripped:
            return stripped
    return ""


def normalize_prompt_line(text: str, limit: int = 220) -> str:
    line = first_nonempty_line(text).lstrip("›>").strip()
    line = " ".join(line.split())
    if len(line) <= limit:
        return line
    return line[: max(0, limit - 1)].rstrip() + "…"


def summarize_prompt(text: str, limit: int = 52) -> str:
    prompt = normalize_prompt_line(text, limit=limit)
    return prompt or ""


def parse_iso_timestamp(value: str) -> int:
    if not value:
        return 0

    try:
        return int(datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp())
    except ValueError:
        return 0


def slugify_project_dir(project_dir: str) -> str:
    normalized = project_dir.strip()
    if not normalized:
        return ""
    if normalized == "/":
        return "-"
    return "-" + normalized.strip("/").replace("/", "-")


def extract_message_text(message: Any) -> str:
    if isinstance(message, str):
        return message
    if not isinstance(message, dict):
        return ""

    content = message.get("content")
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""

    parts: list[str] = []
    for item in content:
        if not isinstance(item, dict):
            continue
        if item.get("type") == "text":
            text = str(item.get("text") or "").strip()
            if text:
                parts.append(text)
    return "\n".join(parts)


def title_label(window_title: str, tool: str) -> str:
    prefix = f"AI:{tool}:"
    if window_title.startswith(prefix):
        return window_title[len(prefix) :].strip() or tool.capitalize()

    if ": " in window_title:
        pathish = window_title.split(": ", 1)[0].strip()
        if pathish:
            return Path(pathish.replace("~", str(HOME))).name or pathish

    if window_title.strip():
        return window_title.strip()

    return tool.capitalize()


def detect_agent(window: dict[str, Any], processes: dict[int, dict[str, Any]], children: dict[int, list[int]]) -> dict[str, Any] | None:
    pid = int(window.get("pid") or 0)
    if pid <= 0:
        return None

    matched: dict[str, Any] | None = None
    for descendant_pid in walk_descendants(pid, children):
        proc = processes.get(descendant_pid)
        if not proc:
            continue
        exe_name = proc.get("exe_name", "")
        if exe_name in AGENT_NAMES:
            matched = {
                "tool": exe_name,
                "agent_pid": descendant_pid,
                "cwd": proc.get("cwd") or "",
                "agent_start_epoch": process_start_epoch(proc),
            }
            break

    if not matched:
        return None

    return {
        "window_id": window.get("id"),
        "window_pid": pid,
        "workspace_id": window.get("workspace_id"),
        "tool": matched["tool"],
        "tool_display": matched["tool"].capitalize(),
        "agent_pid": matched["agent_pid"],
        "cwd": matched["cwd"],
        "agent_start_epoch": matched["agent_start_epoch"],
        "window_title": window.get("title") or "",
        "focused": bool(window.get("is_focused")),
        "position": window.get("layout", {}).get("pos_in_scrolling_layout") or [999, 999],
        "focus_order": (
            int((window.get("focus_timestamp") or {}).get("secs") or 0),
            int((window.get("focus_timestamp") or {}).get("nanos") or 0),
        ),
    }


def query_codex_threads(
    connection: sqlite3.Connection, thread_ids: list[str] | None = None, cwd: str | None = None
) -> dict[str, dict[str, Any]]:
    if thread_ids:
        placeholders = ",".join("?" for _ in thread_ids)
        rows = connection.execute(
            f"""
            SELECT id, cwd, created_at, updated_at, title, first_user_message
            FROM threads
            WHERE id IN ({placeholders})
            """,
            thread_ids,
        ).fetchall()
    elif cwd is not None:
        rows = connection.execute(
            """
            SELECT id, cwd, created_at, updated_at, title, first_user_message
            FROM threads
            WHERE cwd = ? AND archived = 0
            ORDER BY created_at DESC
            LIMIT 16
            """,
            [cwd],
        ).fetchall()
    else:
        return {}

    return {
        str(row["id"]): {
            "thread_id": str(row["id"]),
            "cwd": str(row["cwd"] or ""),
            "created_at": int(row["created_at"] or 0),
            "updated_at": int(row["updated_at"] or 0),
            "prompt_first_line": normalize_prompt_line(str(row["first_user_message"] or row["title"] or "")),
            "label": summarize_prompt(str(row["first_user_message"] or row["title"] or "")),
        }
        for row in rows
    }


def recover_codex_prompts(agents: list[dict[str, Any]]) -> dict[int, dict[str, Any]]:
    result: dict[int, dict[str, Any]] = {}
    if not agents or not CODEX_STATE_DB.exists():
        return result

    try:
        connection = sqlite3.connect(f"file:{CODEX_STATE_DB}?mode=ro", uri=True)
        connection.row_factory = sqlite3.Row
    except sqlite3.Error as error:
        debug(f"failed to open codex db: {error}")
        return result

    try:
        patterns = [f"pid:{int(agent['agent_pid'])}:%" for agent in agents]
        where_clause = " OR ".join("process_uuid LIKE ?" for _ in patterns)
        rows = connection.execute(
            f"""
            SELECT process_uuid, thread_id, MAX(ts) AS last_ts
            FROM logs
            WHERE thread_id != '' AND ({where_clause})
            GROUP BY process_uuid, thread_id
            ORDER BY last_ts DESC
            """,
            patterns,
        ).fetchall()

        latest_thread_by_pid: dict[int, str] = {}
        for row in rows:
            process_uuid = str(row["process_uuid"] or "")
            parts = process_uuid.split(":", 2)
            if len(parts) < 3 or parts[0] != "pid":
                continue
            try:
                pid = int(parts[1])
            except ValueError:
                continue
            if pid not in latest_thread_by_pid:
                latest_thread_by_pid[pid] = str(row["thread_id"] or "")

        direct_threads = query_codex_threads(connection, thread_ids=[thread_id for thread_id in latest_thread_by_pid.values() if thread_id])
        used_thread_ids: set[str] = set()

        for agent in agents:
            agent_pid = int(agent["agent_pid"])
            thread_id = latest_thread_by_pid.get(agent_pid) or ""
            thread = direct_threads.get(thread_id)
            if not thread:
                continue
            result[agent_pid] = thread
            used_thread_ids.add(thread_id)

        unresolved_by_cwd: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for agent in agents:
            if int(agent["agent_pid"]) in result:
                continue
            unresolved_by_cwd[str(agent.get("cwd") or "")].append(agent)

        for cwd, unresolved_agents in unresolved_by_cwd.items():
            if not cwd:
                continue

            candidates = list(query_codex_threads(connection, cwd=cwd).values())
            candidates = [candidate for candidate in candidates if candidate["thread_id"] not in used_thread_ids]
            if not candidates:
                continue

            candidates.sort(key=lambda item: item["created_at"])
            unresolved_agents.sort(key=lambda item: float(item.get("agent_start_epoch") or 0))
            available = candidates[:]

            for agent in unresolved_agents:
                start_epoch = float(agent.get("agent_start_epoch") or 0)
                if not available:
                    break
                selected = min(
                    available,
                    key=lambda item: abs(int(item.get("created_at") or 0) - start_epoch),
                )
                result[int(agent["agent_pid"])] = selected
                used_thread_ids.add(selected["thread_id"])
                available.remove(selected)
    except sqlite3.Error as error:
        debug(f"failed to query codex db: {error}")
    finally:
        connection.close()

    return result


def read_claude_session_file(session_path: Path) -> dict[str, Any]:
    cache_key = str(session_path)
    try:
        stat = session_path.stat()
    except OSError:
        CLAUDE_SESSION_CACHE.pop(cache_key, None)
        return {}

    cached = CLAUDE_SESSION_CACHE.get(cache_key)
    if cached and cached.get("mtime_ns") == stat.st_mtime_ns and cached.get("size") == stat.st_size:
        return dict(cached.get("data") or {})

    data = {
        "session_id": session_path.stem,
        "project_dir": "",
        "created_at": 0,
        "updated_at": int(stat.st_mtime),
        "prompt_first_line": "",
        "label": "",
        "last_prompt": "",
    }

    try:
        with session_path.open("r", encoding="utf-8", errors="ignore") as handle:
            for raw_line in handle:
                raw_line = raw_line.strip()
                if not raw_line:
                    continue

                try:
                    entry = json.loads(raw_line)
                except json.JSONDecodeError:
                    continue

                if not data["project_dir"]:
                    data["project_dir"] = str(entry.get("cwd") or "")

                timestamp = parse_iso_timestamp(str(entry.get("timestamp") or ""))
                if timestamp:
                    data["updated_at"] = max(int(data["updated_at"]), timestamp)

                if entry.get("type") == "last-prompt":
                    last_prompt = normalize_prompt_line(str(entry.get("lastPrompt") or ""))
                    if last_prompt:
                        data["last_prompt"] = last_prompt
                    continue

                if data["prompt_first_line"]:
                    continue

                if entry.get("type") != "user":
                    continue
                if entry.get("parentUuid") is not None:
                    continue

                prompt_text = normalize_prompt_line(extract_message_text(entry.get("message")))
                if not prompt_text:
                    continue

                data["prompt_first_line"] = prompt_text
                data["label"] = summarize_prompt(prompt_text)
                if timestamp:
                    data["created_at"] = timestamp
    except OSError:
        return {}

    if not data["prompt_first_line"] and data["last_prompt"]:
        data["prompt_first_line"] = data["last_prompt"]
        data["label"] = summarize_prompt(data["last_prompt"])

    CLAUDE_SESSION_CACHE[cache_key] = {
        "mtime_ns": stat.st_mtime_ns,
        "size": stat.st_size,
        "data": dict(data),
    }
    return data


def load_claude_sessions(project_dir: str) -> list[dict[str, Any]]:
    slug = slugify_project_dir(project_dir)
    if not slug:
        return []

    project_path = CLAUDE_PROJECTS_DIR / slug
    if not project_path.is_dir():
        return []

    sessions: list[dict[str, Any]] = []
    for session_path in sorted(project_path.glob("*.jsonl")):
        session = read_claude_session_file(session_path)
        if not session:
            continue
        if session.get("project_dir") and session.get("project_dir") != project_dir:
            continue
        sessions.append(session)

    sessions.sort(key=lambda item: int(item.get("created_at") or 0))
    return sessions


def recover_claude_prompts(agents: list[dict[str, Any]]) -> dict[int, dict[str, Any]]:
    result: dict[int, dict[str, Any]] = {}
    if not agents:
        return result

    agents_by_cwd: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for agent in agents:
        agents_by_cwd[str(agent.get("cwd") or "")].append(agent)

    for cwd, cwd_agents in agents_by_cwd.items():
        if not cwd:
            continue

        sessions = load_claude_sessions(cwd)
        if not sessions:
            continue

        cwd_agents.sort(key=lambda item: float(item.get("agent_start_epoch") or 0))
        available = list(sessions)

        for agent in cwd_agents:
            if not available:
                break

            start_epoch = float(agent.get("agent_start_epoch") or 0)
            selected = min(
                available,
                key=lambda item: abs(int(item.get("created_at") or 0) - start_epoch),
            )

            created_at = int(selected.get("created_at") or 0)
            if created_at and start_epoch and abs(created_at - start_epoch) > 6 * 3600:
                continue

            result[int(agent["agent_pid"])] = selected
            available.remove(selected)

    return result


def build_recovered_prompts(agents: list[dict[str, Any]]) -> dict[int, dict[str, Any]]:
    recovered: dict[int, dict[str, Any]] = {}
    codex_agents = [agent for agent in agents if agent.get("tool") == "codex"]
    claude_agents = [agent for agent in agents if agent.get("tool") == "claude"]
    recovered.update(recover_codex_prompts(codex_agents))
    recovered.update(recover_claude_prompts(claude_agents))
    return recovered


def finalize_agent(agent: dict[str, Any], recovered: dict[int, dict[str, Any]]) -> dict[str, Any]:
    window_pid = int(agent.get("window_pid") or 0)
    metadata = parse_session_file(window_pid)
    recovered_data = recovered.get(int(agent.get("agent_pid") or 0), {})
    window_title = str(agent.get("window_title") or "")
    tool = str(agent.get("tool") or "agent")

    project_dir = (
        metadata.get("project_dir")
        or str(recovered_data.get("project_dir") or "")
        or str(agent.get("cwd") or "")
    )
    label = (
        metadata.get("label")
        or str(recovered_data.get("label") or "")
        or title_label(window_title, tool)
    )
    prompt_first_line = (
        metadata.get("prompt_first_line")
        or str(recovered_data.get("prompt_first_line") or "")
        or label
    )

    return {
        "window_id": agent.get("window_id"),
        "workspace_id": agent.get("workspace_id"),
        "tool": tool,
        "tool_display": agent.get("tool_display"),
        "label": label,
        "prompt_first_line": prompt_first_line,
        "project_dir": project_dir,
        "window_title": window_title,
        "focused": bool(agent.get("focused")),
        "position": agent.get("position"),
        "focus_order": agent.get("focus_order"),
    }


def build_state(windows: list[dict[str, Any]], workspaces: list[dict[str, Any]]) -> dict[str, Any]:
    processes, children = read_proc_table()
    windows_by_id = {window.get("id"): window for window in windows}
    agents_by_workspace: dict[int, list[dict[str, Any]]] = defaultdict(list)
    detected_agents: list[dict[str, Any]] = []

    for window in windows:
        if window.get("app_id") != "Alacritty":
            continue
        agent = detect_agent(window, processes, children)
        if not agent:
            continue
        detected_agents.append(agent)

    recovered_prompts = build_recovered_prompts(detected_agents)

    for agent in detected_agents:
        workspace_id = int(agent.get("workspace_id") or -1)
        if workspace_id < 0:
            continue
        agents_by_workspace[workspace_id].append(finalize_agent(agent, recovered_prompts))

    workspace_states: list[dict[str, Any]] = []
    focused_workspace: dict[str, Any] | None = None

    for workspace in sorted(workspaces, key=lambda item: int(item.get("idx") or 0)):
        workspace_id = int(workspace.get("id") or -1)
        active_window = windows_by_id.get(workspace.get("active_window_id"))
        active_window_title = ""
        if active_window:
            active_window_title = active_window.get("title") or ""

        agents = agents_by_workspace.get(workspace_id, [])
        agents.sort(
            key=lambda item: (
                0 if item.get("focused") else 1,
                int((item.get("position") or [999, 999])[0]),
                int((item.get("position") or [999, 999])[1]),
                -int(item.get("focus_order", (0, 0))[0]),
                -int(item.get("focus_order", (0, 0))[1]),
            )
        )

        if agents:
            primary = agents[0]
            summary_text = f"{primary['tool']}: {primary['label']}"
            if len(agents) > 1:
                summary_text += f" +{len(agents) - 1}"
            primary_prompt_line = primary.get("prompt_first_line") or primary.get("label") or ""
        else:
            primary = None
            summary_text = active_window_title or "No active window"
            primary_prompt_line = ""

        workspace_state = {
            "id": workspace_id,
            "idx": workspace.get("idx"),
            "name": workspace.get("name") or str(workspace.get("idx") or workspace_id),
            "is_focused": bool(workspace.get("is_focused")),
            "active_window_id": workspace.get("active_window_id"),
            "active_window_title": active_window_title or "No active window",
            "summary_text": summary_text,
            "primary_prompt_line": primary_prompt_line,
            "agent_count": len(agents),
            "agents": agents,
        }
        workspace_states.append(workspace_state)

        if workspace_state["is_focused"]:
            focused_workspace = workspace_state

    if focused_workspace is None and workspace_states:
        focused_workspace = next((item for item in workspace_states if item["agent_count"] > 0), workspace_states[0])

    return {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "focused_workspace": focused_workspace,
        "workspaces": workspace_states,
    }


def write_state(state: dict[str, Any], previous_payload: str | None) -> str:
    payload = json.dumps(state, separators=(",", ":"), ensure_ascii=True)
    if payload == previous_payload:
        return payload

    temp_path = STATE_FILE.with_suffix(".tmp")
    temp_path.write_text(json.dumps(state, indent=2) + "\n")
    temp_path.replace(STATE_FILE)
    return payload


def main() -> int:
    ensure_dirs()
    last_payload: str | None = None

    try:
        while True:
            cleanup_sessions()
            windows = run_json(["niri", "msg", "-j", "windows"])
            workspaces = run_json(["niri", "msg", "-j", "workspaces"])
            last_payload = write_state(build_state(windows, workspaces), last_payload)
            time.sleep(1.0)
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
