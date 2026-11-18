"""Tests for plan.py. Run from the repository root:

    python3 -m unittest discover -s .github/scripts -p 'test_*.py'

testdata/actions-2026-11-18.json holds this repository's queued and running runs and their jobs as
the Actions API returned them on 2026-11-18 (trimmed to the fields plan.py reads), and the jobs of
one completed CI run.
"""

import io
import json
import os
import re
import tempfile
import unittest
import urllib.error
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

import plan

HERE = Path(__file__).resolve().parent
WORKFLOWS = HERE.parent / "workflows"
TABLE = json.loads((HERE.parent / "ci-pools.json").read_text())
RECORDED = json.loads((HERE / "testdata" / "actions-2026-11-18.json").read_text())
NOW = 1_800_000_000.0
MAC = "namespace-profile-macos-26"
LINUX = "nscloud-ubuntu-24.04-amd64-4x8"

ALL = {
    "app": "true",
    "next-ci": "true",
    "next-app": "true",
    "next-app-native": "true",
    "next-app-platforms": "true",
    "next-app-platforms-native": "true",
    "next-app-tools-platforms": "true",
    "next-app-tools-wsl": "true",
    "next-app-tools-pack": "true",
}


def env(changes=None, **extra):
    base = {
        "GITHUB_REPOSITORY": "stdotwork/pluxy",
        "GITHUB_RUN_ID": "1",
        "EVENT": "merge_group",
        "HEAD_REPO": "",
        "ACTOR": "someone",
        "CHANGES": json.dumps(ALL if changes is None else changes),
        "PLUXY_MACOS_CLOUD_RUNNER": MAC,
        "PLUXY_LINUX_CLOUD_RUNNER": "",
        "PLUXY_POOL_STATUS": "",
        "PLUXY_LINUX_RUNNER": "",
        "PLUXY_WINDOWS_RUNNER": "",
    }
    base.update(extra)
    return base


def iso(t):
    from datetime import datetime, timezone

    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def api(jobs_by_run, statuses=None):
    """A fake fetch serving runs (all reported in_progress unless statuses says) and their jobs."""
    calls = []

    def fetch(path, params):
        calls.append((path, dict(params)))
        if path.endswith("/actions/runs"):
            runs = [
                {"id": rid, "status": (statuses or {}).get(rid, "in_progress"), "created_at": iso(NOW - 600)}
                for rid in jobs_by_run
                if (statuses or {}).get(rid, "in_progress") == params["status"]
            ]
            return {"total_count": len(runs), "workflow_runs": runs}
        rid = int(path.split("/")[-2])
        return {"total_count": len(jobs_by_run[rid]), "jobs": jobs_by_run[rid]}

    fetch.log = calls
    return fetch


def recorded_fetch(path, params):
    if path.endswith("/actions/runs"):
        return RECORDED["runs"][params["status"]]
    return RECORDED["jobs"][path.split("/")[-2]]


def job(labels, status="queued", started=None, created=NOW - 300):
    return {
        "labels": labels,
        "status": status,
        "started_at": iso(started) if started is not None else None,
        "created_at": iso(created),
    }


def run_plan(e, fetch=None, now=NOW):
    return plan.plan(e, fetch or api({}), now, TABLE)


def picks_of(runners, prefix):
    return {k: v for k, v in runners.items() if k.startswith(prefix)}


class Medians(unittest.TestCase):
    def test_own_entry_family_and_default(self):
        self.assertEqual(plan.median(TABLE, "next-app.yml/native/usage", "namespace"), 12.1)
        self.assertEqual(plan.median(TABLE, "next-app.yml/checks", "github"), 8.1)
        # No self-hosted numbers yet: GitHub's.
        self.assertEqual(plan.median(TABLE, "next-app.yml/checks", "self-hosted"), 8.1)
        # Unknown job: default, on every pool.
        self.assertEqual(plan.median(TABLE, "next-app-platforms.yml/app/windows-latest", "github"), 10.0)
        table = {"default": 3, "medians": {"a.yml/native": {"github": 7}}}
        self.assertEqual(plan.median(table, "a.yml/native/x", "namespace"), 7.0)
        self.assertEqual(plan.median(table, "a.yml/nativex", "github"), 3.0)


class Catalogue(unittest.TestCase):
    def test_keys(self):
        keys = [j.key for j in plan.catalogue(ALL)]
        self.assertEqual(
            keys,
            [
                "next-app.yml/checks",
                "next-app.yml/native/usage",
                "next-app.yml/native/tools",
                "next-app.yml/native/app",
                "next-app.yml/native/release",
                "next-ci.yml/next",
                "next-app-platforms.yml/app/windows-latest",
                "next-app-platforms.yml/app/macos-latest",
                "next-app-platforms.yml/native/windows-latest/platform",
                "next-app-platforms.yml/native/windows-latest/package",
                "next-app-platforms.yml/native/macos-latest/platform",
                "next-app-platforms.yml/native/macos-latest/package",
                "next-app-platforms.yml/dialogs/windows-latest",
                "next-app-platforms.yml/dialogs/macos-latest",
                "next-app-tools-platforms.yml/tools/windows-latest",
                "next-app-tools-platforms.yml/tools/macos-latest",
                "next-app-tools-pack.yml/build/darwin/arm64,darwin/amd64",
            ],
        )

    def test_skipped_workflows_and_shards(self):
        changes = dict(ALL, **{"next-app-native": "false", "next-app-platforms-native": "false", "next-ci": "false"})
        keys = [j.key for j in plan.catalogue(changes)]
        self.assertNotIn("next-ci.yml/next", keys)
        self.assertFalse([k for k in keys if "/native/" in k])
        self.assertIn("next-app-platforms.yml/dialogs/macos-latest", keys)
        self.assertEqual(plan.catalogue({}), [])

    def test_workflows_use_these_keys_and_matrices(self):
        """Ties the catalogue to the workflow files: each routed job reads its key from
        inputs.runners, and the matrices have the legs the catalogue names."""
        text = {f: (WORKFLOWS / f).read_text() for f in ("next-app.yml", "next-ci.yml", "next-app-platforms.yml", "next-app-tools-platforms.yml", "next-app-tools-pack.yml")}
        lookups = {
            "next-app.yml": ["['next-app.yml/checks']", "[format('next-app.yml/native/{0}', matrix.shard)]"],
            "next-ci.yml": ["['next-ci.yml/next']"],
            "next-app-platforms.yml": [
                "[format('next-app-platforms.yml/app/{0}', matrix.os)]",
                "[format('next-app-platforms.yml/native/{0}/{1}', matrix.os, matrix.shard)]",
                "[format('next-app-platforms.yml/dialogs/{0}', matrix.os)]",
            ],
            "next-app-tools-platforms.yml": ["[format('next-app-tools-platforms.yml/tools/{0}', matrix.os)]"],
            "next-app-tools-pack.yml": ["[format('next-app-tools-pack.yml/build/{0}', matrix.platform)]"],
        }
        for name, needles in lookups.items():
            self.assertTrue(re.search(r"\n      runners:\n        description: .*\n        type: string\n        default: ''\n", text[name]), name)
            for needle in needles:
                self.assertTrue("fromJSON(inputs.runners || '{}')" + needle in text[name], f"{name}: {needle}")
        shards = re.search(r"shard: \[([^\]]*)\]", text["next-app.yml"]).group(1)
        self.assertEqual(tuple(s.strip() for s in shards.split(",")), plan.LINUX_NATIVE_SHARDS)
        native = text["next-app-platforms.yml"].split("\n  native:\n")[1].split("\n  dialogs:\n")[0]
        self.assertEqual(tuple(re.findall(r"^          - (\w+)$", native.split("shard:")[1], re.M)), plan.PLATFORM_SHARDS)
        self.assertIn("platform: 'darwin/arm64,darwin/amd64'", text["next-app-tools-pack.yml"])


class Scoring(unittest.TestCase):
    def test_idle_pools_put_the_longest_mac_job_on_namespace(self):
        runners, _ = run_plan(env({"next-app-platforms": "true", "next-app-platforms-native": "true"}))
        # package: Namespace 1.5 + 7.0 = 8.5 against GitHub's 18.7. The others then wait for the
        # one Namespace Mac (app: 8.5 + 1.5 + 2.4 = 12.4 against 4.6) and go to GitHub.
        self.assertEqual(runners["next-app-platforms.yml/native/macos-latest/package"], MAC)
        for key in ("app/macos-latest", "native/macos-latest/platform", "dialogs/macos-latest"):
            self.assertEqual(runners[f"next-app-platforms.yml/{key}"], "macos-latest", key)
        for key in ("app/windows-latest", "native/windows-latest/platform", "native/windows-latest/package", "dialogs/windows-latest"):
            self.assertEqual(runners[f"next-app-platforms.yml/{key}"], "windows-latest", key)

    def test_github_macos_backlog_moves_jobs_to_namespace(self):
        changes = {"next-app-platforms": "true"}
        runners, _ = run_plan(env(changes))
        # app: Namespace 1.5 + 2.4 = 3.9 against 4.6; dialogs: 3.9 + 1.5 + 2.0 = 7.4 against 2.9.
        self.assertEqual(runners["next-app-platforms.yml/app/macos-latest"], MAC)
        self.assertEqual(runners["next-app-platforms.yml/dialogs/macos-latest"], "macos-latest")
        # Five macOS jobs queued on GitHub fill its five macOS slots for 5 minutes each: dialogs on
        # GitHub would finish at 5 + 2.9 = 7.9, after Namespace's 7.4.
        busy = api({10: [job(["macos-latest"]) for _ in range(5)]})
        runners, lines = run_plan(env(changes), busy)
        self.assertEqual(runners["next-app-platforms.yml/dialogs/macos-latest"], MAC)
        self.assertIn("| `next-app-platforms.yml/dialogs/macos-latest` | `\"namespace-profile-macos-26\"` | 7.4 | 7.9 |  |", lines)

    def test_github_total_slots(self):
        # 20 Linux jobs that started just now fill all of GitHub's slots for 16 minutes, and a
        # Windows job then waits as long.
        full = api({10: [job(["ubuntu-24.04"], "in_progress", started=NOW) for _ in range(20)]})
        runners, lines = run_plan(env({"next-app-tools-platforms": "true"}), full)
        self.assertEqual(runners["next-app-tools-platforms.yml/tools/windows-latest"], "windows-latest")
        self.assertIn("| `next-app-tools-platforms.yml/tools/windows-latest` | `\"windows-latest\"` |  | 26.0 |  |", lines)
        # macOS: the free macOS slots don't help, the total is full; Namespace wins.
        self.assertEqual(runners["next-app-tools-platforms.yml/tools/macos-latest"], MAC)

    def test_running_namespace_mac_job(self):
        # A job on the Namespace Mac for 1 minute of its assumed 2.3: free in 1.3.
        running = api({10: [job([MAC], "in_progress", started=NOW - 60)]})
        _, lines = run_plan(env({"next-app-tools-platforms": "true"}), running)
        # tools: 1.3 + 1.5 + 0.6 = 3.4 against GitHub's 1.6.
        self.assertIn("| `next-app-tools-platforms.yml/tools/macos-latest` | `\"macos-latest\"` | 3.4 | 1.6 |  |", lines)

    def test_namespace_linux(self):
        runners, _ = run_plan(env(PLUXY_LINUX_CLOUD_RUNNER=json.dumps(LINUX)))
        linux = picks_of(runners, "next-")
        # checks 0.3 + 5.6 against 8.1; app 0.3 + 18.0 against 17.5.
        self.assertEqual(linux["next-app.yml/checks"], LINUX)
        self.assertEqual(linux["next-ci.yml/next"], LINUX)
        self.assertEqual(linux["next-app.yml/native/tools"], LINUX)
        self.assertEqual(linux["next-app.yml/native/app"], "ubuntu-24.04")
        # An array label passes through as an array.
        runners, _ = run_plan(env(PLUXY_LINUX_CLOUD_RUNNER='["nscloud", "x"]'))
        self.assertEqual(runners["next-app.yml/checks"], ["nscloud", "x"])

    def test_namespace_linux_capacity(self):
        # Eight 4-vCPU slots, all busy with jobs that started now (13 minutes each): checks on
        # Namespace would finish at 13 + 0.3 + 5.6 = 18.9, after GitHub's 8.1.
        full = api({10: [job([LINUX], "in_progress", started=NOW) for _ in range(8)]})
        runners, _ = run_plan(env({"next-app": "true"}, PLUXY_LINUX_CLOUD_RUNNER=json.dumps(LINUX)), full)
        self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04")
        seven = api({10: [job([LINUX], "in_progress", started=NOW) for _ in range(7)]})
        runners, _ = run_plan(env({"next-app": "true"}, PLUXY_LINUX_CLOUD_RUNNER=json.dumps(LINUX)), seven)
        self.assertEqual(runners["next-app.yml/checks"], LINUX)

    def test_ties_go_namespace_then_github(self):
        table = {"default": 5, "medians": {}, "pools": {"namespace-macos": {"slots": 1, "startup": 0}}}
        runners, _ = plan.plan(env({"next-app-tools-pack": "true"}), api({}), NOW, table)
        self.assertEqual(runners["next-app-tools-pack.yml/build/darwin/arm64,darwin/amd64"], MAC)


class Capacity(unittest.TestCase):
    def pools(self, e):
        notes = []
        pools = plan.build_pools(TABLE, e, RECORDED["recorded_at"], notes)
        load = plan.read_load(recorded_fetch, e, RECORDED["recorded_at"], notes, float("inf"))
        plan.add_load(pools, load, RECORDED["recorded_at"])
        return pools, load

    def test_recorded_counts(self):
        # Counted from the recording with jq: GitHub 20 running (2 macOS) and 62 queued (8 macOS);
        # 2 running on the maintainers' pluxy-linux runners.
        pools, load = self.pools(env())
        self.assertEqual(len(load), 84)
        self.assertEqual((pools["github"].running, pools["github"].queued), (20, 62))
        self.assertEqual(pools["self-linux"].running, 2)
        self.assertEqual(pools["namespace-macos"].running + pools["namespace-macos"].queued, 0)

    def test_own_run_left_out(self):
        own = "44225844116"  # 18 queued or running jobs, 5 of them running
        pools, load = self.pools(env(GITHUB_RUN_ID=own))
        self.assertEqual(len(load), 84 - 18)
        self.assertEqual(pools["github"].running, 20 - 5)

    def test_recorded_saturation_routes_macos_to_namespace(self):
        # GitHub had all 20 slots taken and 62 jobs queued, so its wait is far longer than the
        # Namespace Mac's queue of this run's own jobs.
        e = env(GITHUB_RUN_ID="1", PLUXY_LINUX_CLOUD_RUNNER=json.dumps(LINUX))
        runners, lines = plan.plan(e, recorded_fetch, RECORDED["recorded_at"], TABLE)
        for key, value in runners.items():
            if "macos" in key or "darwin" in key:
                self.assertEqual(value, MAC, key)
            elif "windows" in key:
                self.assertEqual(value, "windows-latest", key)
            else:
                self.assertEqual(value, LINUX, key)

    def test_request_count_and_window(self):
        fetch = api({rid: [job(["ubuntu-24.04"])] for rid in range(10, 15)})
        run_plan(env(), fetch)
        self.assertEqual(len(fetch.log), 2 + 5)
        listing = [p for path, p in fetch.log if path.endswith("/actions/runs")]
        self.assertEqual([p["status"] for p in listing], ["in_progress", "queued"])
        self.assertEqual(listing[0]["created"], ">=" + iso(NOW - 6 * 3600))
        # No routed job, no API call.
        fetch = api({10: []})
        run_plan(env({}), fetch)
        self.assertEqual(fetch.log, [])

    def test_runs_beyond_the_cap_are_left_out(self):
        fetch = api({rid: [job(["ubuntu-24.04"])] for rid in range(100, 100 + plan.MAX_RUNS + 5)})
        _, lines = run_plan(env(), fetch)
        self.assertEqual(len(fetch.log), 2 + plan.MAX_RUNS)
        self.assertTrue(any("read the jobs of the 40 oldest" in line for line in lines))


class SelfHosted(unittest.TestCase):
    full = staticmethod(lambda: api({10: [job(["ubuntu-24.04"], "in_progress", started=NOW) for _ in range(20)]}))

    def status(self, **fields):
        return json.dumps(dict({"t": NOW, "linux": 1, "windows": 1}, **fields))

    def test_default_job_takes_an_idle_runner_when_github_is_full(self):
        runners, _ = run_plan(env({"next-app": "true", "next-app-native": "false"}, PLUXY_POOL_STATUS=self.status()), self.full())
        self.assertEqual(runners["next-app.yml/checks"], ["self-hosted", "pluxy-linux"])
        # The labels come from PLUXY_LINUX_RUNNER when set.
        runners, _ = run_plan(
            env({"next-app": "true", "next-app-native": "false"}, PLUXY_POOL_STATUS=self.status(), PLUXY_LINUX_RUNNER='["self-hosted", "box"]'),
            self.full(),
        )
        self.assertEqual(runners["next-app.yml/checks"], ["self-hosted", "box"])

    def test_tie_with_github_goes_to_github(self):
        runners, _ = run_plan(env({"next-app": "true", "next-app-native": "false"}, PLUXY_POOL_STATUS=self.status()))
        self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04")

    def test_stale_or_garbage_status_means_no_idle_runner(self):
        for raw in (
            self.status(t=NOW - 181),
            "not json",
            "[]",
            '{"t": "now", "linux": 3}',
            '{"t": true, "linux": 3}',
            '{"linux": 3}',
            json.dumps({"t": NOW, "linux": "3"}),
            json.dumps({"t": NOW, "linux": -2}),
        ):
            runners, lines = run_plan(env({"next-app": "true", "next-app-native": "false"}, PLUXY_POOL_STATUS=raw), self.full())
            self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04", raw)
        runners, _ = run_plan(env({"next-app": "true", "next-app-native": "false"}, PLUXY_POOL_STATUS=self.status(t=NOW - 179)), self.full())
        self.assertEqual(runners["next-app.yml/checks"], ["self-hosted", "pluxy-linux"])

    def test_listed_jobs_only_in_list_order(self):
        linux = {"next-app": "true", "next-app-native": "true"}
        # Unlisted shards never go there; listed ones do, the first listed first.
        a = self.status(jobs={"linux": ["next-app.yml/native/usage", "next-app.yml/checks"]})
        runners, _ = run_plan(env(linux, PLUXY_POOL_STATUS=a), self.full())
        # usage: self-hosted 17.0 against GitHub's 16 + 17.0; checks then waits for that runner
        # (17.0 + 8.1 = 25.1) and goes to GitHub (16 + 8.1 = 24.1).
        self.assertEqual(runners["next-app.yml/native/usage"], ["self-hosted", "pluxy-linux"])
        self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04")
        b = self.status(jobs={"linux": ["next-app.yml/checks", "next-app.yml/native/usage"]})
        runners, _ = run_plan(env(linux, PLUXY_POOL_STATUS=b), self.full())
        self.assertEqual(runners["next-app.yml/checks"], ["self-hosted", "pluxy-linux"])
        self.assertEqual(runners["next-app.yml/native/usage"], ["self-hosted", "pluxy-linux"])
        for shard in ("tools", "app", "release"):
            self.assertEqual(runners[f"next-app.yml/native/{shard}"], "ubuntu-24.04")
        # An empty list lets no job there.
        runners, _ = run_plan(env(linux, PLUXY_POOL_STATUS=self.status(jobs={"linux": []})), self.full())
        self.assertNotIn(["self-hosted", "pluxy-linux"], list(runners.values()))

    def test_never_for_jobs_that_need_github(self):
        status = self.status(windows=9, jobs={"windows": ["next-app-platforms.yml/app/windows-latest"]})
        full = api({10: [job(["windows-latest"], "in_progress", started=NOW) for _ in range(20)]})
        runners, _ = run_plan(env({"next-app-platforms": "true", "next-app-tools-platforms": "true"}, PLUXY_POOL_STATUS=status), full)
        self.assertEqual(runners["next-app-platforms.yml/app/windows-latest"], "windows-latest")
        status = self.status(windows=9)
        runners, _ = run_plan(env({"next-app-platforms": "true", "next-app-tools-platforms": "true"}, PLUXY_POOL_STATUS=status), full)
        self.assertEqual(runners["next-app-tools-platforms.yml/tools/windows-latest"], ["self-hosted", "pluxy-windows"])
        for key, value in runners.items():
            if key != "next-app-tools-platforms.yml/tools/windows-latest":
                self.assertNotIn("self-hosted", value, key)

    def test_queued_self_hosted_job_takes_the_idle_runner(self):
        load = api({10: [job(["ubuntu-24.04"], "in_progress", started=NOW) for _ in range(20)] + [job(["self-hosted", "pluxy-linux"])]})
        runners, _ = run_plan(env({"next-app": "true", "next-app-native": "false"}, PLUXY_POOL_STATUS=self.status()), load)
        # The queued job holds the one idle runner for 16 minutes: 16 + 8.1, a tie with GitHub.
        self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04")

    def test_events_that_may_not_use_it(self):
        changes = {"next-app": "true", "next-app-native": "false"}
        status = self.status()
        for extra in (
            {"EVENT": "pull_request", "HEAD_REPO": "someone/fork"},
            {"EVENT": "pull_request", "HEAD_REPO": "stdotwork/pluxy", "ACTOR": "dependabot[bot]"},
        ):
            runners, _ = run_plan(env(changes, PLUXY_POOL_STATUS=status, **extra), self.full())
            self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04", extra)
        runners, _ = run_plan(env(changes, PLUXY_POOL_STATUS=status, EVENT="pull_request", HEAD_REPO="stdotwork/pluxy"), self.full())
        self.assertEqual(runners["next-app.yml/checks"], ["self-hosted", "pluxy-linux"])


class KillSwitches(unittest.TestCase):
    def test_namespace_linux_unset_or_bad(self):
        for value in ("", "  ", "nscloud-ubuntu-24.04-amd64-4x8", '{"a": 1}', "[]", '[""]', "7"):
            runners, lines = run_plan(env(PLUXY_LINUX_CLOUD_RUNNER=value))
            self.assertEqual(runners["next-app.yml/checks"], "ubuntu-24.04", value)
            self.assertEqual(runners["next-ci.yml/next"], "ubuntu-latest", value)
            self.assertTrue(any("Namespace Linux is off" in line for line in lines), value)

    def test_namespace_macos_unset(self):
        busy = api({10: [job(["macos-latest"]) for _ in range(20)]})
        runners, _ = run_plan(env(PLUXY_MACOS_CLOUD_RUNNER=""), busy)
        for key, value in runners.items():
            if "macos" in key or "darwin" in key:
                self.assertEqual(value, "macos-latest", key)

    def test_bad_self_hosted_label_falls_back(self):
        _, lines = run_plan(env(PLUXY_LINUX_RUNNER="self-hosted"))
        self.assertTrue(any("PLUXY_LINUX_RUNNER is not a JSON runs-on value" in line for line in lines))


class FailSafe(unittest.TestCase):
    def main(self, e, fetch=None, pools=None):
        with tempfile.TemporaryDirectory() as tmp:
            out, summary = os.path.join(tmp, "out"), os.path.join(tmp, "summary")
            e = dict(e, GITHUB_OUTPUT=out, GITHUB_STEP_SUMMARY=summary, POOLS_FILE=pools or str(HERE.parent / "ci-pools.json"))
            with redirect_stdout(io.StringIO()):
                runners = plan.main(e, fetch or api({}), NOW)
            lines = Path(out).read_text().splitlines()
            self.assertEqual(len(lines), 1)
            self.assertTrue(lines[0].startswith("runners="))
            self.assertEqual(json.loads(lines[0][len("runners="):]), runners)
            return runners, Path(summary).read_text()

    def test_api_failure_keeps_the_picks_without_load(self):
        def down(path, params):
            raise urllib.error.URLError("down")

        runners, summary = self.main(env(), down)
        self.assertEqual(runners, plan.plan(env(), api({}), NOW, TABLE)[0])
        self.assertIn("error reading the load", summary)

    def test_api_failure_part_way_keeps_what_was_read(self):
        jobs = {10: [job(["macos-latest"]) for _ in range(5)], 11: [job(["macos-latest"])]}
        good = api(jobs)

        def flaky(path, params):
            if path.endswith("/11/jobs"):
                raise TimeoutError("slow")
            return good(path, params)

        _, lines = run_plan(env({"next-app-platforms": "true"}), flaky)
        self.assertIn("| github | 20 | 0 | 5 |", lines)
        self.assertTrue(any("TimeoutError: slow" in line for line in lines))

    def test_deadline(self):
        with mock.patch.object(plan, "API_DEADLINE", -1):
            _, lines = run_plan(env(), api({10: [job(["macos-latest"])]}))
        self.assertTrue(any("ran out of time" in line for line in lines))

    def test_call_cap(self):
        class Response(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

        fetch = plan.github_fetch({"GITHUB_TOKEN": "x"})
        with mock.patch.object(plan.urllib.request, "urlopen", side_effect=lambda *a, **k: Response(b"{}")):
            for _ in range(plan.MAX_CALLS):
                fetch("/x", {})
            with self.assertRaises(RuntimeError):
                fetch("/x", {})

    def test_errors_give_an_empty_map(self):
        runners, summary = self.main(env(), pools="/nonexistent/ci-pools.json")
        self.assertEqual(runners, {})
        self.assertIn("Error: FileNotFoundError", summary)
        runners, summary = self.main(env(CHANGES="not json"))
        self.assertEqual(runners, {})
        with tempfile.NamedTemporaryFile("w", suffix=".json") as bad:
            bad.write('{"medians": {"next-app.yml/checks": {"github": "slow"}}}')
            bad.flush()
            runners, summary = self.main(env(), pools=bad.name)
        self.assertEqual(runners, {})
        self.assertIn("Error: ValueError", summary)
        with mock.patch.object(plan, "assign", side_effect=KeyError("boom")):
            runners, summary = self.main(env())
        self.assertEqual(runners, {})

    def test_summary_lists_every_job(self):
        runners, summary = self.main(env())
        for key in runners:
            self.assertIn(f"| `{key}` |", summary)
        self.assertEqual(len(runners), len(plan.catalogue(ALL)))


if __name__ == "__main__":
    unittest.main()
