import { expect, test } from "bun:test";
import { onDisposableRunner } from "../native/runner";

// Which runners the Windows and macOS native tests start on (native/runner.ts).

// As a Namespace macOS runner showed it in this repo's canary.
const cloud = {
  GITHUB_ACTIONS: "true",
  RUNNER_ENVIRONMENT: "self-hosted",
  RUNNER_NAME: "nsc-runner-sjv55ml7fcgn0",
  PLUXY_EPHEMERAL_CI_RUNNER: "namespace-profile-macos-26",
  NAMESPACE_GITHUB_RUNTIME: "1",
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

test("a Namespace runner the workflow names passes", () => {
  expect(onDisposableRunner(cloud)).toBe(true);
  expect(onDisposableRunner({ ...cloud, PLUXY_EPHEMERAL_CI_RUNNER: "namespace-profile-mac-large-2" })).toBe(
    true,
  );
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

test("a maintainer's runner with its own name or without Namespace's variable is refused", () => {
  // Its own name and no Namespace variable.
  const own = { ...cloud, RUNNER_NAME: "pluxy-runner-1", NAMESPACE_GITHUB_RUNTIME: undefined };
  expect(onDisposableRunner(own)).toBe(false);
  // Named like Namespace's runners but without Namespace's variable.
  expect(onDisposableRunner({ ...cloud, NAMESPACE_GITHUB_RUNTIME: undefined })).toBe(false);
  expect(onDisposableRunner({ ...cloud, NAMESPACE_GITHUB_RUNTIME: "" })).toBe(false);
  // Namespace's variable copied onto a runner with its own name.
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: "mac-mini-1" })).toBe(false);
  expect(onDisposableRunner({ ...cloud, RUNNER_NAME: undefined })).toBe(false);
});

test("only a Namespace profile label counts", () => {
  for (const label of [
    "1",
    "self-hosted",
    "mac-mini",
    "macos-latest",
    "nscloud-macos-tahoe-arm64-6x14",
    "namespace-profile-",
    "namespace-profile-Macos-26",
    "namespace-profile-macos-26;container.privileged=true",
    "namespace-profile-macos 26",
    "blacksmith-6vcpu-macos-26",
  ])
    expect(onDisposableRunner({ ...cloud, PLUXY_EPHEMERAL_CI_RUNNER: label })).toBe(false);
});

test("the runner's name must be nsc-runner- and the instance id", () => {
  for (const name of [
    "nsc-runner-",
    "nsc-runner-abc",
    "nsc-runner-SJV55ML7FCGN0",
    "nsc-runner-sjv55ml7-fcgn0",
    "nsc-runner-sjv55ml7fcgn0\n",
    "my-nsc-runner-sjv55ml7fcgn0",
    "namespace-profile-macos-26-sjv55ml7fcgn0",
  ])
    expect(onDisposableRunner({ ...cloud, RUNNER_NAME: name })).toBe(false);
});
