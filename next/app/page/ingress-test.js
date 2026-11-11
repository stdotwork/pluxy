(async () => {
  parent.postMessage({ ingress: { boot: true } }, "*");
  try {
    // Only served by native-test. A sandboxed frame posts directly to the native scheme.
    const base = "pluxy-page://localhost";
    const info = await (await fetch(`${base}/frame`)).json();
    const generation = info.generation ?? 0;
    const answers = new Map();
    const delay = (ms) => new Promise((r) => setTimeout(r, ms));
    const report = (v) => parent.postMessage({ ingress: v }, "*");
    const call = (id, method = "theme.get", params = null) => ({ v: 0, id, method, params });
    async function raw(type, data, gen = generation) {
      const r = await fetch(`${base}/send`, {
        method: "POST",
        body: JSON.stringify({ type, data, generation: gen }),
      });
      return { status: r.status, body: await r.text() };
    }
    async function answer(id) {
      for (let i = 0; i < 500; i++) {
        if (answers.has(id)) return answers.get(id);
        await delay(1);
      }
      throw new Error(`missing answer: ${id}`);
    }
    if (location.search) {
      report({ navigated: await raw("call", call("after-navigation")) });
      return;
    }
    window.addEventListener("message", async (e) => {
      if (e.data?.type === "welcome" && e.ports[0])
        e.ports[0].onmessage = (m) => answers.set(m.data.id, m.data);
      if (!e.data?.ingressCommand) return;
      try {
        if (e.data.ingressCommand === "denials") {
          const results = {};
          results.unknown = await raw("call", call("unknown", "missing.method"));
          results.schema = await raw("call", { ...call("schema"), extra: true });
          results.params = await raw("call", call("params", "config.get", {}));
          results.large = await raw(
            "call",
            call("large", "config.set", { values: { report: "x".repeat(65536) } }),
          );
          results.body = await raw(
            "call",
            call("body", "config.set", { values: { report: "x".repeat(262144) } }),
          );
          results.generation = await raw("call", call("generation"), String(Number(generation) + 1));
          results.hello = await raw("hello", { pluxy: "page", type: "hello", v: 0, versions: [0] });
          const r = await raw("call", call("denial-control", "config.get"));
          results.control = { ...r, answer: await answer("denial-control") };
          report({ denials: results });
        }
        if (e.data.ingressCommand === "flight")
          report({
            flight: await Promise.all(
              Array.from({ length: 12 }, (_, i) => raw("call", call(`flight-${i}`, "config.get"))),
            ),
          });
        if (e.data.ingressCommand === "rate") {
          const results = [];
          const start = performance.now();
          for (let i = 0; i < 40; i++) {
            const id = `rate-${i}`;
            const r = await raw("call", call(id));
            if (r.status === 204) await answer(id);
            results.push(r);
          }
          report({ rate: results, elapsed: performance.now() - start });
        }
        if (e.data.ingressCommand === "navigate") location.href = "/ingress-test?second";
        if (e.data.ingressCommand === "closed")
          report({ closed: await raw("closed", null), after: await raw("call", call("after-close")) });
        if (e.data.ingressCommand === "recovery") {
          const r = await raw("call", call("recover", "config.get"));
          report({ recovery: r, answer: await answer("recover") });
        }
        if (e.data.ingressCommand === "control") {
          const r = await raw(
            "call",
            call("final-control", "config.set", { values: { report: "native-ingress-control" } }),
          );
          report({ control: r, answer: await answer("final-control") });
        }
      } catch (e) {
        report({ error: String(e) });
      }
    });
    parent.postMessage({ pluxy: "page", type: "hello", v: 0, versions: [0] }, "*");
  } catch (e) {
    parent.postMessage({ ingress: { error: String(e) } }, "*");
  }
})();
