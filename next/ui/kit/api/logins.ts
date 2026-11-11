// Login methods and the login lifecycle (next/README.md, Accounts added by login).

import type { Host } from "../host";
import { one } from "./path";

export interface LoginInput {
  name: string;
  label?: string;
  secret?: boolean;
  optional?: boolean;
}

export interface LoginMethod {
  method: string;
  kind: "browser" | "device" | "api_key" | string;
  label?: string;
  inputs?: LoginInput[];
}

export interface LoginMethodsExtension {
  extension: string;
  methods: LoginMethod[];
}

export interface LoginPrompt {
  kind: "browser" | "device" | "input" | string;
  message?: string;
  url?: string;
  user_code?: string;
  verification_uri?: string;
  verification_uri_complete?: string;
  expires_in?: number;
  inputs?: LoginInput[];
}

export interface LoginError {
  code: string;
  message: string;
}

export interface Login {
  id: string;
  account: string;
  extension: string;
  method: string;
  state: "pending" | "done" | "failed" | "cancelled" | string;
  prompt?: LoginPrompt;
  error?: LoginError;
}

export const loginsApi = (host: Host) => ({
  loginMethods: () => host.call<{ extensions: LoginMethodsExtension[] }>("GET", "/login-methods"),
  startLogin: (request: { id: string; extension: string; method: string; input?: Record<string, string> }) =>
    host.call<{ login: Login }>("POST", "/accounts", request),
  signInAgain: (id: string, method: string, input?: Record<string, string>) =>
    host.call<{ login: Login }>("POST", `/accounts/${one(id)}/login`, { method, input }),
  login: (id: string) => host.call<{ login: Login }>("GET", `/logins/${one(id)}`),
  submitLoginInput: (id: string, input: Record<string, string>) =>
    host.call<void>("POST", `/logins/${one(id)}/input`, { input }),
  cancelLogin: (id: string) => host.call<void>("DELETE", `/logins/${one(id)}`),
});
