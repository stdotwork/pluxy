// Whether this machine is a disposable CI runner, the only kind the Windows and macOS native tests
// (platform.ts, dialogs.ts) run on: they write the account's own registry or LaunchAgents folder
// and app folders, which on a person's or a maintainer's machine are theirs.
//
// Two kinds pass:
//
// - a GitHub-hosted runner: GITHUB_ACTIONS=true and RUNNER_ENVIRONMENT=github-hosted;
// - a cloud provider's one-job runner that the workflow names
//   (.github/workflows/next-app-platforms.yml). These report themselves self-hosted, so the job
//   opts in: on the jobs it sends to the cloud runner, and only there, it sets
//   PLUXY_EPHEMERAL_CI_RUNNER to the runner label it asked for. The runner's own name must then
//   start with that label and a dash, as the provider names the machine it makes for one job. Any
//   other runner is refused, a maintainer's self-hosted one included, whatever the variable says.

/** The environment variables the check reads. */
export type RunnerEnv = Readonly<Record<string, string | undefined>>;

/** What a runner must be, for the refusal messages. */
export const DISPOSABLE_RUNNER =
  "a disposable CI runner: GitHub-hosted (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted), " +
  "or a cloud runner the workflow names in PLUXY_EPHEMERAL_CI_RUNNER (its label, which RUNNER_NAME starts with)";

/** Whether env is that of a disposable CI runner, as this file's header says. */
export function onDisposableRunner(env: RunnerEnv = process.env): boolean {
  if (env.GITHUB_ACTIONS !== "true") return false;
  if (env.RUNNER_ENVIRONMENT === "github-hosted") return true;
  const label = env.PLUXY_EPHEMERAL_CI_RUNNER ?? "";
  const name = env.RUNNER_NAME ?? "";
  return label !== "" && name.length > label.length + 1 && name.startsWith(`${label}-`);
}
