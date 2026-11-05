import { pollWhileActive } from '../services/windowActivity';
import { useEffect, useState } from 'react';
import { loadHomeOverview, type HomeOverviewSnapshot } from '../services/homeOverview';

// `remote` is null until the connection mode is known; nothing loads before then.
export function useHomeOverview(coreReady: boolean, contextKey: string, remote: boolean | null = false) {
  const [state, setState] = useState<{ scope: string; snapshot: HomeOverviewSnapshot } | null>(null);
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const scope = `${coreReady}:${contextKey}`;

  useEffect(() => {
    if (remote === null) { setLoading(false); return; }
    let active = true;
    let inFlight = false;
    const load = async (showLoading = false) => {
      if (inFlight) return;
      inFlight = true;
      if (showLoading) setLoading(true);
      try {
        const snapshot = await loadHomeOverview(coreReady, Date.now(), remote);
        if (active) setState((previous) => ({
          scope,
          snapshot: {
            ...snapshot,
            models: previous?.scope === scope && JSON.stringify(previous.snapshot.models) === JSON.stringify(snapshot.models)
              ? previous.snapshot.models : snapshot.models,
          },
        }));
      } finally {
        inFlight = false;
        if (active) setLoading(false);
      }
    };
    setLoading(true);
    const stopPolling = pollWhileActive(() => { void load(); }, 60_000);
    return () => { active = false; stopPolling(); };
  }, [coreReady, remote, scope, revision]);

  return {
    snapshot: state?.scope === scope ? state.snapshot : null,
    loading,
    refresh: () => setRevision((value) => value + 1),
  };
}
