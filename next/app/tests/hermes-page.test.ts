// The Hermes Agent page's form logic (src/hermesForm.ts): the model it starts
// from, and which model IDs Apply takes.
import { describe, expect, test } from "bun:test";
import { modelOf, plainModel, validModel, yaml11NotText } from "../src/hermesForm";

describe("model", () => {
  test("starts from pluxy's default model, and from none for a refused or unset file", () => {
    expect(modelOf({ state: "configured", model: "gpt-6.1-sol" })).toBe("gpt-6.1-sol");
    expect(modelOf({ state: "not-configured", model: null })).toBe("");
    expect(modelOf({ state: "refused", reason: "not YAML" })).toBe("");
  });

  test("takes one model ID, without spaces, control characters or ${", () => {
    for (const ok of ["gpt-6.1-sol", "qwen3:8b", "openrouter/x", "a,b", "yesterday"])
      expect(validModel(ok)).toBe(true);
    for (const bad of ["", "a b", "x\n", "a\u0007b", "a\u0085b", "a\u00a0b", "a$" + "{B}", "x".repeat(241)]) {
      expect(validModel(bad)).toBe(false);
      expect(plainModel(bad)).toBe(false);
    }
    expect(validModel("é".repeat(240))).toBe(true);
  });

  test("refuses an ID Hermes reads as something other than text, as hermes-setup does", () => {
    // hermes-setup's own cases (hermes_tests.rs): text in YAML 1.1.
    for (const text of [
      "0a1b2c3d4e5f60718293a4b5c6d7e8f9",
      "deadbeef0123",
      "123e4567-e89b-12d3-a456-426614174000",
      "4o",
      "0o",
      "1.5-pro",
      "2026-11-14-preview",
      "yEs",
      "12e",
      "0x",
      "-",
      ".5x",
      "custom:pluxy",
      "chat_completions",
      "gpt-6.1-sol",
      "qwen3:8b",
    ]) {
      expect([text, yaml11NotText(text)]).toEqual([text, false]);
    }
    // Not text in YAML 1.1: a model ID the add-on refuses, though it is plain.
    for (const other of [
      "y",
      "N",
      "Yes",
      "OFF",
      "~",
      "Null",
      "=",
      "0",
      "-12",
      "+7",
      "1_000",
      "0b101",
      "0x1F",
      "0o17",
      "017",
      "1:30",
      "190:20:30",
      "1.5",
      "-1.",
      "1e5",
      "6.8523015e+5",
      ".5",
      ".5e+3",
      "1:30.5",
      ".inf",
      "-.Inf",
      ".NaN",
      "2026-11-14",
      "2001-12-14t21:59:43.10-05:00",
      "2001-12-14T21:59:43Z",
      "2001-1-4T1:59:43",
    ]) {
      expect([other, yaml11NotText(other)]).toEqual([other, true]);
      expect([other, plainModel(other), validModel(other)]).toEqual([other, true, false]);
    }
    expect(yaml11NotText("")).toBe(true);
  });
});
