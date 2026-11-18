// Whether this machine is a disposable CI runner, the only kind the Windows and macOS native tests
// (platform.ts, dialogs.ts) run on: they write the account's own registry or LaunchAgents folder
// and app folders, which on a person's or a maintainer's machine are theirs.
//
// Two kinds pass:
//
// - a GitHub-hosted runner: GITHUB_ACTIONS=true and RUNNER_ENVIRONMENT=github-hosted;
// - one of Namespace's runners, an ephemeral VM, when the workflow names it
//   (.github/workflows/next-app-platforms.yml). These report themselves self-hosted, so the job
//   opts in: on the jobs it sends to the cloud runner, and only there, it sets
//   PLUXY_EPHEMERAL_CI_RUNNER to the runner label it asked for. The label must be a Namespace
//   runner profile, bare (namespace-profile-<name>, without ;key=value settings), the runner's own
//   name nsc-runner-<instance id>, and Namespace's own variable NAMESPACE_GITHUB_RUNTIME must be
//   set. Namespace's docs give the runner name and call profile runners ephemeral; the variable is
//   what its runners showed in this repo's canary, each up for seconds when its job started, and
//   the docs don't promise one job per VM. If these change, this refuses, and the jobs fail rather
//   than run elsewhere.
//
// A runner's name and labels are whatever its owner registers, so the name proves only which pool
// the job went to, not that the machine is thrown away afterwards. That is why the provider is
// named here rather than taken from the variable: a maintainer's own runner, or a pool of runners
// on a maintainer's machine, does not pass whatever the variable says, unless it is registered
// under Namespace's names and sets Namespace's variable: a tripwire against a mistake, not a guard
// against a runner registered to pass for Namespace's. Another provider is a change to this file,
// with its review.

/** The environment variables the check reads. */
export type RunnerEnv = Readonly<Record<string, string | undefined>>;

/** What a runner must be, for the refusal messages. */
export const DISPOSABLE_RUNNER =
  "a disposable CI runner: GitHub-hosted (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted), " +
  "or a Namespace runner the workflow names in PLUXY_EPHEMERAL_CI_RUNNER (see native/runner.ts)";

/** A Namespace runner profile's label, such as namespace-profile-macos-26. */
const NAMESPACE_PROFILE_LABEL = /^namespace-profile-[a-z0-9][a-z0-9-]*$/;
/** The name Namespace gives the runner it starts for a job, such as nsc-runner-sjv55ml7fcgn0. */
const NAMESPACE_RUNNER_NAME = /^nsc-runner-[a-z0-9]{8,}$/;

/** Whether env is that of a disposable CI runner, as this file's header says. */
export function onDisposableRunner(env: RunnerEnv = process.env): boolean {
  if (env.GITHUB_ACTIONS !== "true") return false;
  if (env.RUNNER_ENVIRONMENT === "github-hosted") return true;
  return (
    NAMESPACE_PROFILE_LABEL.test(env.PLUXY_EPHEMERAL_CI_RUNNER ?? "") &&
    NAMESPACE_RUNNER_NAME.test(env.RUNNER_NAME ?? "") &&
    (env.NAMESPACE_GITHUB_RUNTIME ?? "") !== ""
  );
}
