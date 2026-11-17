// Whether this machine is a disposable CI runner, the only kind the Windows and macOS native tests
// (platform.ts, dialogs.ts) run on: they write the account's own registry or LaunchAgents folder
// and app folders, which on a person's or a maintainer's machine are theirs.
//
// Two kinds pass:
//
// - a GitHub-hosted runner: GITHUB_ACTIONS=true and RUNNER_ENVIRONMENT=github-hosted;
// - one of Blacksmith's macOS runners, a VM made for one job, when the workflow names it
//   (.github/workflows/next-app-platforms.yml). These report themselves self-hosted, so the job
//   opts in: on the jobs it sends to the cloud runner, and only there, it sets
//   PLUXY_EPHEMERAL_CI_RUNNER to the runner label it asked for. The label must be one of
//   Blacksmith's macOS labels (blacksmith-<n>vcpu-macos-<version>), and the runner's own name that
//   label, a dash and the VM's id, as Blacksmith names them.
//
// A runner's name and labels are whatever its owner registers, so the name proves only which pool
// the job went to, not that the machine is thrown away afterwards. That is why the provider is
// named here rather than taken from the variable: a maintainer's own runner, or a pool of runners
// on a maintainer's machine, does not pass whatever the variable says, unless it is registered
// under Blacksmith's names. Another provider is a change to this file, with its review.

/** The environment variables the check reads. */
export type RunnerEnv = Readonly<Record<string, string | undefined>>;

/** What a runner must be, for the refusal messages. */
export const DISPOSABLE_RUNNER =
  "a disposable CI runner: GitHub-hosted (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted), " +
  "or a Blacksmith macOS runner the workflow names in PLUXY_EPHEMERAL_CI_RUNNER (see native/runner.ts)";

/** Blacksmith's macOS runner labels, such as blacksmith-6vcpu-macos-26. */
const BLACKSMITH_MACOS_LABEL = /^blacksmith-[0-9]+vcpu-macos-[0-9][a-z0-9.]*$/;
/** The id Blacksmith puts after the label in a runner's name, such as y4lzca6yzyu5qsma. */
const BLACKSMITH_VM_ID = /^[a-z0-9]+$/;

/** Whether env is that of a disposable CI runner, as this file's header says. */
export function onDisposableRunner(env: RunnerEnv = process.env): boolean {
  if (env.GITHUB_ACTIONS !== "true") return false;
  if (env.RUNNER_ENVIRONMENT === "github-hosted") return true;
  const label = env.PLUXY_EPHEMERAL_CI_RUNNER ?? "";
  const name = env.RUNNER_NAME ?? "";
  return (
    BLACKSMITH_MACOS_LABEL.test(label) &&
    name.startsWith(`${label}-`) &&
    BLACKSMITH_VM_ID.test(name.slice(label.length + 1))
  );
}
