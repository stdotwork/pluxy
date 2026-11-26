// useLoad's states, driven through Preact's DOM renderer with a fake host: never loaded, loaded,
// loaded and empty, and a reload that failed after data was already there.
import { afterEach, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import type { Reply } from "../kit/host";
import { type Kit, KitContext, type Loaded, useLoad } from "../kit/ui";
import { mountIn, restoreDom, settle } from "./fakeDom";

afterEach(restoreDom);

type Items = { items: string[] };

/** Replies a test hands out one by one; a reply of "pending" never arrives. */
function mountHook(replies: (Reply<Items> | "pending")[]) {
  const seen: Loaded<Items>[] = [];
  const signedOut = { times: 0 };
  const kit = {
    unauthorized: () => {
      signedOut.times++;
    },
  } as Kit;
  function Probe() {
    const loaded = useLoad<Items>(() => {
      const next = replies.shift();
      if (!next) throw new Error("no reply left for this read");
      return next === "pending" ? new Promise<Reply<Items>>(() => {}) : Promise.resolve(next);
    });
    seen.push(loaded);
    return <p>{loaded.phase}</p>;
  }
  return {
    seen,
    signedOut,
    last: () => {
      const loaded = seen[seen.length - 1];
      if (!loaded) throw new Error("nothing rendered yet");
      return loaded;
    },
    mount: () =>
      mountIn(
        <KitContext.Provider value={kit}>
          <Probe />
        </KitContext.Provider>,
      ),
  };
}

const ok = (...items: string[]): Reply<Items> => ({ status: 200, body: { items } });
const hiccup: Reply<Items> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };

async function reload(hook: { last(): Loaded<Items> }) {
  await act(async () => hook.last().reload());
  await settle();
}

test("before the first reply the hook says it has not loaded, and holds no data", async () => {
  const hook = mountHook(["pending"]);
  await hook.mount();
  expect(hook.last()).toMatchObject({ phase: "loading", loading: true });
  expect(hook.last().data).toBeUndefined();
  expect(hook.last().error).toBeUndefined();
});

test("a good reply is loaded, and an empty answer is loaded too", async () => {
  const hook = mountHook([ok("a", "b")]);
  await hook.mount();
  expect(hook.last()).toMatchObject({ phase: "ready", loading: false, data: { items: ["a", "b"] } });

  const empty = mountHook([ok()]);
  await empty.mount();
  expect(empty.last()).toMatchObject({ phase: "ready", loading: false, data: { items: [] } });
});

test("a first load that fails has the error and no data", async () => {
  const hook = mountHook([hiccup]);
  await hook.mount();
  expect(hook.last()).toMatchObject({ phase: "failed", loading: false, error: "core hiccup" });
  expect(hook.last().data).toBeUndefined();
});

test("a reload that fails keeps the data already loaded and reports the error", async () => {
  const hook = mountHook([ok("a", "b"), hiccup]);
  await hook.mount();
  await reload(hook);
  expect(hook.last()).toMatchObject({
    phase: "failed",
    loading: false,
    error: "core hiccup",
    data: { items: ["a", "b"] },
  });
});

test("a reload that works after a failed one is loaded again, with the new data and no error", async () => {
  const hook = mountHook([ok("a"), hiccup, ok("c")]);
  await hook.mount();
  await reload(hook);
  await reload(hook);
  expect(hook.last()).toMatchObject({ phase: "ready", data: { items: ["c"] } });
  expect(hook.last().error).toBeUndefined();
});

test("while a reload runs the data stays, and it is loading again", async () => {
  const hook = mountHook([ok("a"), "pending"]);
  await hook.mount();
  await reload(hook);
  expect(hook.last()).toMatchObject({ phase: "ready", loading: true, data: { items: ["a"] } });
});

test("a refused key goes to unauthorized and the data stays", async () => {
  const hook = mountHook([ok("a"), { status: 401 }]);
  await hook.mount();
  await reload(hook);
  expect(hook.signedOut.times).toBe(1);
  expect(hook.last()).toMatchObject({ phase: "ready", data: { items: ["a"] } });
});
