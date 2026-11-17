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

test("a cloud runner the workflow names passes", () => {
  expect(onDisposableRunner(cloud)).toBe(true);
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

test("the runner's name must be the label, a dash and the machine's own part", () => {
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: label })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}-` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `${label}x-6vzq7pn7rqfrtrja` })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: `my-${label}-6vzq7pn7rqfrtrja` })).toBe(false);
});
