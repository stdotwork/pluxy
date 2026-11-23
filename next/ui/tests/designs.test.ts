import { expect, test } from "bun:test";
import { currentDesign, DESIGN_KEY, designAddress } from "../kit/designs";

class MemoryStorage {
  values = new Map<string, string>();
  getItem(name: string) {
    return this.values.get(name) ?? null;
  }
  setItem(name: string, value: string) {
    this.values.set(name, value);
  }
  removeItem(name: string) {
    this.values.delete(name);
  }
}

test("with nothing asked and nothing remembered there is no design", () => {
  const store = new MemoryStorage();
  expect(currentDesign("", store)).toBeUndefined();
  expect(currentDesign("?safe", store)).toBeUndefined();
  expect(store.values.size).toBe(0);
});

test("the address picks a design and it is remembered", () => {
  const store = new MemoryStorage();
  expect(currentDesign("?design=b", store)).toBe("b");
  expect(store.values.get("pluxy.design")).toBe("b");
  expect(DESIGN_KEY).toBe("pluxy.design");
  // The next load, with no parameter, is still in it.
  expect(currentDesign("", store)).toBe("b");
  // Another one asked for replaces it.
  expect(currentDesign("?safe&design=c", store)).toBe("c");
  expect(currentDesign("", store)).toBe("c");
});

test("design=off forgets the design", () => {
  const store = new MemoryStorage();
  store.setItem("pluxy.design", "a");
  expect(currentDesign("?design=off", store)).toBeUndefined();
  expect(store.values.has("pluxy.design")).toBe(false);
  expect(currentDesign("", store)).toBeUndefined();
});

test("a value that is no design is ignored, in the address and in storage", () => {
  const store = new MemoryStorage();
  store.setItem("pluxy.design", "a");
  expect(currentDesign("?design=z", store)).toBe("a");
  expect(currentDesign("?design=", store)).toBe("a");
  expect(currentDesign("?design=A", store)).toBe("a");
  expect(store.values.get("pluxy.design")).toBe("a");
  store.setItem("pluxy.design", "zebra");
  expect(currentDesign("", store)).toBeUndefined();
});

test("storage that refuses or is missing still lets the address pick for this load", () => {
  const refusing = {
    getItem(): string | null {
      throw new Error("blocked");
    },
    setItem() {
      throw new Error("blocked");
    },
    removeItem() {
      throw new Error("blocked");
    },
  };
  expect(currentDesign("?design=a", refusing)).toBe("a");
  expect(currentDesign("", refusing)).toBeUndefined();
  expect(currentDesign("?design=off", refusing)).toBeUndefined();
  expect(currentDesign("?design=c", undefined)).toBe("c");
  expect(currentDesign("", undefined)).toBeUndefined();
});

test("a switcher address sets the parameter and keeps the page", () => {
  expect(designAddress("http://127.0.0.1:9000/panel/?safe#/accounts/acct-1", "b")).toBe(
    "http://127.0.0.1:9000/panel/?safe=&design=b#/accounts/acct-1",
  );
  expect(designAddress("http://127.0.0.1:9000/panel/?design=a#/usage", "off")).toBe(
    "http://127.0.0.1:9000/panel/?design=off#/usage",
  );
});
