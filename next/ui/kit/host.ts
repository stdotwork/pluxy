// What a host gives the kit. The browser panel (panel/browserHost.ts) and the desktop app each
// implement it; the kit's pages and shell use nothing else to reach core or keep settings.

export type Method = "GET" | "POST" | "PATCH" | "DELETE" | "PUT";

/** A management API reply. status 0 means core could not be reached. */
export interface Reply<T> {
  status: number;
  body?: T;
  error?: { code: string; message: string };
}

/** Things a host may or may not offer; pages hide what the host lacks. */
export type Feature = "signOut";

export interface Host {
  /**
   * Calls core's management API, version 0. path is under /v0/management, such as "/accounts".
   * The host adds the management key; the kit never sees it, and no request carrying it goes
   * anywhere but core's management API.
   */
  call<T>(method: Method, path: string, body?: unknown): Promise<Reply<T>>;
  /** Whether the host holds a management key to call with. */
  signedIn(): boolean;
  /**
   * Checks key against core and keeps it when core accepts it. remember keeps it after the
   * window closes. Returns why it was refused, or undefined.
   */
  signIn(key: string, remember: boolean): Promise<string | undefined>;
  /** Forgets the key. Also called by the kit when core answers 401. */
  signOut(): void;
  /** Small named settings the person chose, kept on this device. */
  prefs: {
    get(name: string): string | null;
    set(name: string, value: string | null): void;
  };
  features: ReadonlySet<Feature>;
}
