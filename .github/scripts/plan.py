#!/usr/bin/env python3
"""Picks the runner for each heavy job of a CI run: ci.yml's plan job.

For every routed job the run will start, it estimates when the job would finish on each pool it
may use (the job's median run time there, from .github/ci-pools.json, plus the wait for a free
slot) and picks the earliest. The wait comes from this repository's queued and running jobs, read
through the Actions API, and each pool's capacity. Jobs are placed longest first, each one adding
to its pool's load. Ties, and pools with no data, go Namespace, then GitHub, then self-hosted.

It writes `runners`, a JSON object from job key to runs-on value, to GITHUB_OUTPUT, and a table of
its picks to GITHUB_STEP_SUMMARY. It never fails the job: an error it cannot work around gives
`runners={}`, and the called workflows then pick their runners as they did before the plan; an API
error leaves the load it could not read out, and is logged. Standard library only.
"""

import heapq
import json
import os
import sys
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone

# next/app/native/shards.ts names these; a shard missing here falls back to the workflow's own
# runner.
LINUX_NATIVE_SHARDS = ("usage", "tools", "app", "release")
PLATFORM_SHARDS = ("platform", "package")
OSES = ("windows-latest", "macos-latest")

SELF_LABELS = {"linux": ["self-hosted", "pluxy-linux"], "windows": ["self-hosted", "pluxy-windows"]}
STATUS_MAX_AGE = 180  # seconds

MAX_CALLS = 50
MAX_RUNS = 40
LOOKBACK = 6 * 3600  # seconds
API_DEADLINE = 90  # seconds for all API calls
REQUEST_TIMEOUT = 15  # seconds


class Job:
    """A routed job: its key, operating system, GitHub label, and where self-hosted may take it.

    self_hosted is "default" (allowed unless PLUXY_POOL_STATUS lists the pool's jobs), "listed"
    (only when listed) or "never" (its test runs only on a runner that reports itself
    GitHub-hosted, or the pool has no such operating system).
    """

    def __init__(self, key, os_, github, namespace=True, self_hosted="never"):
        self.key = key
        self.os = os_
        self.github = github
        self.namespace = namespace
        self.self_hosted = self_hosted


def catalogue(changes):
    """The routed jobs this run starts, from the changes job's outputs."""
    on = lambda name: changes.get(name) == "true"  # noqa: E731
    jobs = []
    if on("next-app"):
        jobs.append(Job("next-app.yml/checks", "linux", "ubuntu-24.04", self_hosted="default"))
        if on("next-app-native"):
            for shard in LINUX_NATIVE_SHARDS:
                # The document portal needs /dev/fuse: Namespace has it; a self-hosted runner
                # only when its supervisor lists the shard.
                jobs.append(Job(f"next-app.yml/native/{shard}", "linux", "ubuntu-24.04", self_hosted="listed"))
    if on("next-ci"):
        jobs.append(Job("next-ci.yml/next", "linux", "ubuntu-latest", self_hosted="listed"))
    if on("next-app-platforms"):
        # Windows: the registry tests, and the native and dialogs tests, need GitHub's runner.
        for os_ in OSES:
            jobs.append(Job(f"next-app-platforms.yml/app/{os_}", os_name(os_), os_, namespace=os_ == "macos-latest"))
        if on("next-app-platforms-native"):
            for os_ in OSES:
                for shard in PLATFORM_SHARDS:
                    jobs.append(Job(f"next-app-platforms.yml/native/{os_}/{shard}", os_name(os_), os_, namespace=os_ == "macos-latest"))
        for os_ in OSES:
            jobs.append(Job(f"next-app-platforms.yml/dialogs/{os_}", os_name(os_), os_, namespace=os_ == "macos-latest"))
    if on("next-app-tools-platforms"):
        jobs.append(Job("next-app-tools-platforms.yml/tools/windows-latest", "windows", "windows-latest", namespace=False, self_hosted="default"))
        jobs.append(Job("next-app-tools-platforms.yml/tools/macos-latest", "macos", "macos-latest"))
    if on("next-app-tools-pack"):
        jobs.append(Job("next-app-tools-pack.yml/build/darwin/arm64,darwin/amd64", "macos", "macos-latest"))
    return jobs


def os_name(label):
    return label.split("-")[0]


def median(table, key, kind):
    """The job's median minutes on a pool kind: its own entry, else its family's, else default."""
    medians = table.get("medians", {})
    parts = key.split("/")
    for n in range(len(parts), 0, -1):
        entry = medians.get("/".join(parts[:n]))
        if entry is not None:
            value = entry.get(kind, entry.get("github"))
            if value is not None:
                return float(value)
    return float(table.get("default", 10))


def busy(cfg, os_):
    value = cfg.get("busy", 10)
    if isinstance(value, dict):
        value = value.get(os_, 10)
    return float(value)


class Pool:
    """A pool's slots, as a heap of the minutes from now at which each one is free."""

    def __init__(self, name, kind, os_, slots, startup, label, cfg):
        self.name = name
        self.kind = kind
        self.os = os_
        self.label = label
        self.startup = float(startup)
        self.cfg = cfg
        self.slots = [0.0] * max(int(slots), 0)
        self.running = 0
        self.queued = 0

    def start(self, os_):
        return self.slots[0] if self.slots else None

    def take(self, os_, finish):
        heapq.heapreplace(self.slots, finish)


class GitHubPool(Pool):
    """GitHub's runners: slots jobs at once, of which macos_slots may be macOS."""

    def __init__(self, cfg):
        super().__init__("github", "github", None, cfg.get("slots", 20), cfg.get("startup", 0), None, cfg)
        self.macos = [0.0] * max(int(cfg.get("macos_slots", 5)), 0)

    def start(self, os_):
        if not self.slots or (os_ == "macos" and not self.macos):
            return None
        return max(self.slots[0], self.macos[0]) if os_ == "macos" else self.slots[0]

    def take(self, os_, finish):
        heapq.heapreplace(self.slots, finish)
        if os_ == "macos":
            heapq.heapreplace(self.macos, finish)


def runs_on(value):
    """A runs-on value from a repository variable that holds JSON, or None."""
    if not value or not value.strip():
        return None
    parsed = json.loads(value)
    if isinstance(parsed, str) and parsed:
        return parsed
    if isinstance(parsed, list) and parsed and all(isinstance(x, str) and x for x in parsed):
        return parsed
    raise ValueError("not a label or a list of labels")


def labels_of(value):
    return [value] if isinstance(value, str) else list(value)


def pool_status(raw, now, notes):
    """Idle runners and listed jobs per self-hosted pool from PLUXY_POOL_STATUS."""
    idle = {"linux": 0, "windows": 0}
    listed = {}
    if not raw or not raw.strip():
        notes.append("PLUXY_POOL_STATUS is not set: no self-hosted runner is idle")
        return idle, listed
    try:
        status = json.loads(raw)
        t = status["t"]
        if not isinstance(t, (int, float)) or isinstance(t, bool):
            raise ValueError("t is not a number")
    except (ValueError, KeyError, TypeError) as e:
        notes.append(f"PLUXY_POOL_STATUS is unreadable ({e}): no self-hosted runner is idle")
        return idle, listed
    age = now - t
    if age > STATUS_MAX_AGE:
        notes.append(f"PLUXY_POOL_STATUS is {int(age)} s old: no self-hosted runner is idle")
        return idle, listed
    for os_ in idle:
        n = status.get(os_)
        if isinstance(n, int) and not isinstance(n, bool) and n > 0:
            idle[os_] = n
    jobs = status.get("jobs")
    if isinstance(jobs, dict):
        for os_ in idle:
            keys = jobs.get(os_)
            if isinstance(keys, list):
                listed[os_] = [k for k in keys if isinstance(k, str)]
    return idle, listed


def self_hosted_allowed(env):
    """The event check the workflows apply to PLUXY_LINUX_RUNNER and PLUXY_WINDOWS_RUNNER."""
    event = env.get("EVENT", "")
    if event in ("push", "merge_group", "workflow_dispatch"):
        return True
    return (
        event == "pull_request"
        and env.get("HEAD_REPO", "") == env.get("GITHUB_REPOSITORY", "")
        and env.get("ACTOR", "") != "dependabot[bot]"
    )


def build_pools(table, env, now, notes):
    cfgs = table.get("pools", {})
    pools = {"github": GitHubPool(cfgs.get("github", {}))}

    try:
        label = runs_on(env.get("PLUXY_LINUX_CLOUD_RUNNER", ""))
    except ValueError as e:
        label = None
        notes.append(f"PLUXY_LINUX_CLOUD_RUNNER is not a JSON runs-on value ({e}): Namespace Linux is off")
    if label is None:
        if not env.get("PLUXY_LINUX_CLOUD_RUNNER", "").strip():
            notes.append("Namespace Linux is off (PLUXY_LINUX_CLOUD_RUNNER not set)")
    else:
        cfg = cfgs.get("namespace-linux", {})
        slots = int(cfg.get("vcpu", 32)) // max(int(cfg.get("vcpu_per_job", 4)), 1)
        pools["namespace-linux"] = Pool("namespace-linux", "namespace", "linux", slots, cfg.get("startup", 0.3), label, cfg)

    mac = env.get("PLUXY_MACOS_CLOUD_RUNNER", "").strip()
    if mac:
        cfg = cfgs.get("namespace-macos", {})
        pools["namespace-macos"] = Pool("namespace-macos", "namespace", "macos", cfg.get("slots", 1), cfg.get("startup", 1.5), mac, cfg)
    else:
        notes.append("Namespace macOS is off (PLUXY_MACOS_CLOUD_RUNNER not set)")

    idle, listed = pool_status(env.get("PLUXY_POOL_STATUS", ""), now, notes)
    allowed = self_hosted_allowed(env)
    if not allowed:
        notes.append("this event may not use self-hosted runners")
    cfg = cfgs.get("self-hosted", {})
    for os_ in ("linux", "windows"):
        var = f"PLUXY_{os_.upper()}_RUNNER"
        try:
            label = runs_on(env.get(var, "")) or SELF_LABELS[os_]
        except ValueError as e:
            label = SELF_LABELS[os_]
            notes.append(f"{var} is not a JSON runs-on value ({e}): using {json.dumps(label)}")
        pool = Pool(f"self-{os_}", "self-hosted", os_, idle[os_] if allowed else 0, cfg.get("startup", 0), label, cfg)
        pool.listed = listed.get(os_)
        pools[pool.name] = pool
    return pools


def classify(labels, pools):
    """The pool and operating system a job's runs-on labels send it to, or (None, None)."""
    if not labels:
        return None, None
    for name in ("namespace-linux", "namespace-macos", "self-linux", "self-windows"):
        pool = pools.get(name)
        if pool and set(labels_of(pool.label)) <= set(labels) and (pool.kind != "self-hosted" or "self-hosted" in labels):
            return name, pool.os
    if "self-hosted" in labels:
        return ("self-windows", "windows") if any("windows" in x for x in labels) else ("self-linux", "linux")
    first = labels[0]
    if first.startswith("nscloud") or first.startswith("namespace-profile"):
        return ("namespace-macos", "macos") if "macos" in first else ("namespace-linux", "linux")
    for os_ in ("ubuntu", "macos", "windows"):
        if first.startswith(os_):
            return "github", "linux" if os_ == "ubuntu" else os_
    return None, None


def parse_time(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


def read_load(fetch, env, now, notes, deadline):
    """This repository's queued and running jobs from the last LOOKBACK seconds, as dicts with
    labels, status, started (a timestamp or None) and created. Partial on an API error, which it
    logs."""
    repo = env["GITHUB_REPOSITORY"]
    own = str(env.get("GITHUB_RUN_ID", ""))
    since = datetime.fromtimestamp(now - LOOKBACK, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    runs = {}
    jobs = []
    try:
        for status in ("in_progress", "queued"):
            page = 1
            while True:
                if time.monotonic() > deadline:
                    raise TimeoutError("ran out of time listing runs")
                data = fetch(f"/repos/{repo}/actions/runs", {"status": status, "created": f">={since}", "per_page": 100, "page": page})
                batch = data.get("workflow_runs", [])
                for run in batch:
                    if str(run["id"]) != own:
                        runs.setdefault(run["id"], run)
                if len(batch) < 100 or page >= 3:
                    break
                page += 1
        ordered = sorted(runs.values(), key=lambda r: r.get("created_at", ""))
        if len(ordered) > MAX_RUNS:
            notes.append(f"{len(ordered)} active runs; read the jobs of the {MAX_RUNS} oldest")
            ordered = ordered[:MAX_RUNS]
        for run in ordered:
            if time.monotonic() > deadline:
                raise TimeoutError("ran out of time reading jobs")
            page = 1
            while True:
                data = fetch(f"/repos/{repo}/actions/runs/{run['id']}/jobs", {"filter": "latest", "per_page": 100, "page": page})
                batch = data.get("jobs", [])
                for job in batch:
                    if job.get("status") in ("queued", "in_progress", "waiting", "pending", "requested"):
                        jobs.append(
                            {
                                "labels": job.get("labels") or [],
                                "status": job["status"],
                                "started": parse_time(job["started_at"]) if job["status"] == "in_progress" and job.get("started_at") else None,
                                "created": job.get("created_at") or "",
                            }
                        )
                if len(batch) < 100 or page * 100 >= data.get("total_count", 0) or page >= 3:
                    break
                page += 1
    except Exception as e:  # noqa: BLE001: any API failure leaves the rest of the load unread
        notes.append(f"error reading the load, which is left out from here on: {type(e).__name__}: {e}")
    return jobs


def add_load(pools, load, now):
    """Places the jobs already queued or running elsewhere on their pools: running ones first,
    then queued ones in the order they were created."""
    running = [j for j in load if j["status"] == "in_progress"]
    queued = sorted((j for j in load if j["status"] != "in_progress"), key=lambda j: j["created"])
    for job in running + queued:
        name, os_ = classify(job["labels"], pools)
        pool = pools.get(name)
        if pool is None:
            continue
        if job["status"] == "in_progress":
            pool.running += 1
            if pool.kind == "self-hosted":
                continue  # PLUXY_POOL_STATUS's idle count already leaves it out
        else:
            pool.queued += 1
        length = busy(pool.cfg, os_)
        if job["started"] is not None:
            length = max(length - (now - job["started"]) / 60, 1.0)
        start = pool.start(os_)
        if start is not None:
            pool.take(os_, start + length)


def candidates(job, pools):
    """The pools a job may use, in the order that breaks ties: Namespace, GitHub, self-hosted."""
    out = []
    if job.namespace:
        out.append(pools.get(f"namespace-{job.os}"))
    out.append(pools["github"])
    pool = pools.get(f"self-{job.os}")
    if pool is not None and job.self_hosted != "never":
        listed = pool.listed
        if (listed is None and job.self_hosted == "default") or (listed is not None and job.key in listed):
            out.append(pool)
    return [p for p in out if p is not None]


def assign(jobs, pools, table):
    """Places the run's jobs, longest GitHub median first, except that the jobs a self-hosted pool
    lists take their turns in the list's order. Returns {key: (pool, finish per pool)}."""
    order = sorted(jobs, key=lambda j: -median(table, j.key, "github"))
    for name in ("self-linux", "self-windows"):
        listed = pools[name].listed
        if listed:
            turns = [i for i, j in enumerate(order) if j.key in listed]
            ranked = sorted((order[i] for i in turns), key=lambda j: listed.index(j.key))
            for i, job in zip(turns, ranked):
                order[i] = job
    picks = {}
    for job in order:
        finishes = {}
        best = None
        for rank, pool in enumerate(candidates(job, pools)):
            start = pool.start(job.os)
            if start is None:
                continue
            finish = start + pool.startup + median(table, job.key, pool.kind)
            finishes[pool.kind] = finish
            if best is None or (round(finish, 6), rank) < (round(best[1], 6), best[2]):
                best = (pool, finish, rank, start)
        if best is None:
            continue
        pool, finish, _, _ = best
        pool.take(job.os, finish)
        picks[job.key] = (pool, finishes)
    return picks


def plan(env, fetch, now, table):
    """Returns (runners, summary lines)."""
    notes = []
    changes = json.loads(env.get("CHANGES") or "{}")
    jobs = catalogue(changes)
    pools = build_pools(table, env, now, notes)
    load = []
    if jobs:
        deadline = time.monotonic() + API_DEADLINE
        load = read_load(fetch, env, now, notes, deadline)
    add_load(pools, load, now)
    state = {name: (pool.running, pool.queued, len(pool.slots)) for name, pool in pools.items()}
    picks = assign(jobs, pools, table)
    runners = {}
    for job in jobs:
        if job.key in picks:
            pool, _ = picks[job.key]
            runners[job.key] = job.github if pool.kind == "github" else pool.label
    return runners, summary(jobs, picks, runners, state, notes, len(load))


def summary(jobs, picks, runners, state, notes, seen):
    def fmt(x):
        return "" if x is None else f"{x:.1f}"

    lines = ["## Runner plan", ""]
    lines.append(f"{seen} jobs of other runs queued or running were read.")
    lines += ["", "| Pool | Slots | Running | Queued |", "| --- | --- | --- | --- |"]
    for name, (running, queued, slots) in state.items():
        lines.append(f"| {name} | {slots} | {running} | {queued} |")
    lines += ["", "Expected finish in minutes from now, per pool:", ""]
    lines += ["| Job | Pick | Namespace | GitHub | Self-hosted |", "| --- | --- | --- | --- | --- |"]
    for job in jobs:
        finishes = picks.get(job.key, (None, {}))[1]
        pick = json.dumps(runners[job.key]) if job.key in runners else "(none)"
        lines.append(f"| `{job.key}` | `{pick}` | {fmt(finishes.get('namespace'))} | {fmt(finishes.get('github'))} | {fmt(finishes.get('self-hosted'))} |")
    if notes:
        lines += ["", "Notes:", ""] + [f"- {n}" for n in notes]
    lines += ["", "```json", json.dumps(runners, indent=2), "```", ""]
    return lines


def github_fetch(env):
    api = env.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
    token = env.get("GITHUB_TOKEN", "")
    calls = [0]

    def fetch(path, params):
        calls[0] += 1
        if calls[0] > MAX_CALLS:
            raise RuntimeError(f"more than {MAX_CALLS} API calls")
        url = f"{api}{path}?{urllib.parse.urlencode(params)}"
        request = urllib.request.Request(
            url,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {token}",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "ci-runner-plan",
            },
        )
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT) as response:
            return json.load(response)

    fetch.calls = calls
    return fetch


def main(env=None, fetch=None, now=None):
    env = dict(os.environ) if env is None else env
    runners, lines = {}, []
    try:
        with open(env.get("POOLS_FILE", ".github/ci-pools.json"), encoding="utf-8") as f:
            table = json.load(f)
        fetch = fetch or github_fetch(env)
        runners, lines = plan(env, fetch, time.time() if now is None else now, table)
        calls = getattr(fetch, "calls", None)
        if calls:
            lines.append(f"{calls[0]} API calls.")
    except Exception as e:  # noqa: BLE001: the plan must never fail the run
        runners = {}
        lines = ["## Runner plan", "", f"Error: {type(e).__name__}: {e}", "", "No picks: every job uses its workflow's own runner."]
    text = "\n".join(lines) + "\n"
    print(text)
    for name, content in (("GITHUB_OUTPUT", f"runners={json.dumps(runners, separators=(',', ':'))}\n"), ("GITHUB_STEP_SUMMARY", text)):
        path = env.get(name)
        if path:
            try:
                with open(path, "a", encoding="utf-8") as f:
                    f.write(content)
            except OSError as e:
                print(f"could not write {name}: {e}", file=sys.stderr)
    return runners


if __name__ == "__main__":
    main()
