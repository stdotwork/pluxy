// A benign test page (test/alpha): reads and writes its own settings and follows the theme,
// only through the channel. The browser test checks each of these.
import { connect } from "./pluxy-page.js";

const $ = (id) => document.getElementById(id);

async function main() {
  const pluxy = await connect();
  const show = (values) => {
    $("greeting").textContent = values.greeting ?? "(none)";
  };
  $("appearance").textContent = pluxy.theme.appearance;
  pluxy.on("theme", (t) => {
    $("appearance").textContent = t.appearance;
  });
  show((await pluxy.call("config.get")).values);
  $("say").addEventListener("click", async () => {
    show((await pluxy.call("config.set", { values: { greeting: "hello from the page" } })).values);
  });
  $("state").textContent = `connected as ${pluxy.extension}, page ${pluxy.page}`;
}

main().catch((e) => {
  $("state").textContent = `failed: ${e.message}`;
});
