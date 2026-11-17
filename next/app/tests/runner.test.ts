import { expect, test } from "bun:test";
import { onDisposableRunner } from "../native/runner";

// Which runners the Windows and macOS native tests start on (native/runner.ts).

const label = "blacksmith-6vcpu-macos-26";
const cloud = {
  GITHUB_ACTIONS: "true",
  RUNNER_ENVIRONMENT: "self-hosted",
  RUNNER_NAME: `${label}-6vzq7pn7rqfrtrja`,
  PLUXY_EPHEMERAL_CI_RUNNER: label,
};

test("a GitHub-hosted runner passes", () => {
  expect(
    onDisposableRunner({
      GITHUB_ACTIONS: "true",
      RUNNER_ENVIRONMENT: "github-hosted",
      RUNNER_NAME: "GitHub Actions 1000011608",
    }),
  ).toBe(true);
});

test("a Blacksmith macOS runner the workflow names passes", () => {
  expect(onDisposableRunner(cloud)).toBe(true);
  expect(
    onDisposableRunner({
      ...cloud,
      PLUXY_EPHEMERAL_CI_RUNNER: "blacksmith-12vcpu-macos-15",
      RUNNER_NAME: "blacksmith-12vcpu-macos-15-46cmtnbjfuqltbnn",
    }),
  ).toBe(true);
});

test("outside GitHub Actions nothing passes", () => {
  expect(onDisposableRunner({})).toBe(false);
  expect(onDisposableRunner({ RUNNER_ENVIRONMENT: "github-hosted" })).toBe(false);
  expect(onDisposableRunner({ ...cloud, GITHUB_ACTIONS: undefined })).toBe(false);
  expect(onDisposableRunner({ ...cloud, GITHUB_ACTIONS: "false" })).toBe(false);
});

test("a self-hosted runner without the workflow's opt-in is refused", () => {
  expect(onDisposableRunner({ ...cloud, PLUXY_EPHEMERAL_CI_RUNNER: undefined })).toBe(false);
  expect(onDisposableRunner({ ...cloud, PLUXY_EPHEMERAL_CI_RUNNER: "" })).toBe(false);
});

test("a maintainer's self-hosted runner is refused even with the opt-in set", () => {
  // Its name is not the cloud label's.
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: "pluxy-runner-1" })).toBe(false);
  // A bare yes, or a generic label, is no label its name starts with.
  expect(
    onDisposableRunner({ ...cloud, RUNNER_NAME: "pluxy-runner-1", PLUXY_EPHEMERAL_CI_RUNNER: "1" }),
  ).toBe(false);
  expect(
    onDisposableRunner({ ...cloud, RUNNER_NAME: "pluxy-runner-1", PLUXY_EPHEMERAL_CI_RUNNER: "self-hosted" }),
  ).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: undefined })).toBe(false);
});

test("a maintainer's runner named after its own label is refused", () => {
  // A Mac registered as mac-mini-1 with the label mac-mini, the variable set to that label.
  expect(
    onDisposableRunner({ ...cloud, PLUXY_EPHEMERAL_CI_RUNNER: "mac-mini", RUNNER_NAME: "mac-mini-1" }),
  ).toBe(false);
  // A Windows machine registered as windows-1, the variable set to the default label by mistake.
  expect(
    onDisposableRunner({ ...cloud, PLUXY_EPHEMERAL_CI_RUNNER: "windows", RUNNER_NAME: "windows-1" }),
  ).toBe(false);
  // A pool of one-job runners on a maintainer's machine, named <set>-runner-<id>.
  expect(
    onDisposableRunner({
      ...cloud,
      PLUXY_EPHEMERAL_CI_RUNNER: "pluxy-mac",
      RUNNER_NAME: "pluxy-mac-runner-x7k2p",
    }),
  ).toBe(false);
});

test("only Blacksmith's macOS labels count", () => {
  // Windows stays on GitHub's runners, whatever Blacksmith names its Windows machines (this repo's
  // canary saw blacksmith-<id>-<n>vcpu, other repos <label>-<id>).
  expect(
    onDisposableRunner({
      ...cloud,
      PLUXY_EPHEMERAL_CI_RUNNER: "blacksmith-4vcpu-windows-2025",
      RUNNER_NAME: "blacksmith-01m7ncy73eqcfbb73g7wqgs893-4vcpu",
    }),
  ).toBe(false);
  expect(
    onDisposableRunner({
      ...cloud,
      PLUXY_EPHEMERAL_CI_RUNNER: "blacksmith-4vcpu-windows-2025",
      RUNNER_NAME: "blacksmith-4vcpu-windows-2025-01m7ncy73eqcfbb73g7wqgs893",
    }),
  ).toBe(false);
  expect(
    onDisposableRunner({
      ...cloud,
      PLUXY_EPHEMERAL_CI_RUNNER: "blacksmith",
      RUNNER_NAME: "blacksmith-y4lzca6yzyu5qsma",
    }),
  ).toBe(false);
  expect(
    onDisposableRunner({
      ...cloud,
      PLUXY_EPHEMERAL_CI_RUNNER: "Blacksmith-6vcpu-macos-26",
      RUNNER_NAME: "Blacksmith-6vcpu-macos-26-y4lzca6yzyu5qsma",
    }),
  ).toBe(false);
});

test("the runner's name must be the label, a dash and the machine's own part", () => {
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}-runner-y4lzca6yzyu5qsma` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}-Y4LZCA6YZYU5QSMA` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}-y4lz ca6y` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: label })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}-` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}x-6vzq7pn7rqfrtrja` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `my-${label}-6vzq7pn7rqfrtrja` })).toBe(false);
});
