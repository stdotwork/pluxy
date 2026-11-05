import { getPluginSupport, pluginsApi } from './services/plugins';
import { useEffect, useState } from 'react';
import { collectPluginResourceEntries, PLUGIN_RESOURCES_REFRESH_EVENT } from './services/pluginResources';
import {
  readCachedPluginNavEntries,
  toPluginNavEntry,
  writeCachedPluginNavEntries,
  type PluginNavEntry,
} from './services/pluginNavigation';

export function usePluginNavEntries(coreReady: boolean, target: string | null) {
  const [state, setState] = useState<{ target: string; entries: PluginNavEntry[]; fetched: boolean } | null>(null);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    const refresh = () => setRevision((value) => value + 1);
    window.addEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, refresh);
    return () => window.removeEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, refresh);
  }, []);

  useEffect(() => {
    if (!coreReady || !target) {
      setState(null);
      return undefined;
    }
    setState({ target, entries: readCachedPluginNavEntries(target), fetched: false });
    let cancelled = false;
    void (async () => {
      try {
        const supported = await getPluginSupport();
        const data = supported ? await pluginsApi.list() : null;
        if (cancelled) return;
        const next = data?.pluginsEnabled ? collectPluginResourceEntries(data.plugins).map(toPluginNavEntry) : [];
        setState({ target, entries: next, fetched: true });
        writeCachedPluginNavEntries(next, target);
      } catch {
        if (cancelled) return;
        setState({ target, entries: [], fetched: false });
        writeCachedPluginNavEntries([], target);
      }
    })();
    return () => { cancelled = true; };
  }, [coreReady, target, revision]);

  return {
    entries: coreReady && state?.target === target ? state.entries : [],
    fetched: coreReady && state?.target === target && state.fetched,
  };
}
