// Which tool each key was made for, for the Tools page (its keyOwners hook): the app keeps a key it
// makes for a tool in its vault with the tool's name, so the page can name the key. Only names are
// read here, never a key's value (the vault's list holds none).

import { originOf, type Secret } from "./clientPage";
import { nativeReply } from "./toolWords";

/**
 * The tool's name for each key of the pluxy at target, by the key's ID in pluxy (else its masked
 * form). A key made for another pluxy, and a secret the person entered, name nothing here.
 */
export function ownersOf(secrets: Secret[] | undefined, target: string | undefined): Map<string, string> {
  const here = originOf(target);
  const owners = new Map<string, string>();
  if (here === undefined) return owners;
  for (const s of secrets ?? []) {
    const k = s.client_key;
    const key = k?.key_id ?? k?.masked;
    if (!k || !key || !s.label || originOf(k.core?.origin) !== here) continue;
    owners.set(key, s.label);
  }
  return owners;
}

/** The names of this app's keys for the pluxy at target; none when the vault cannot be read. */
export async function readKeyOwners(target: string | undefined): Promise<Map<string, string>> {
  const r = await nativeReply<{ secrets?: Secret[] }>("vault_secrets").catch(() => undefined);
  return ownersOf(r?.body?.secrets, target);
}
