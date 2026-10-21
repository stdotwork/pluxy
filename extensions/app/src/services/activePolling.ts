export type ActivitySource = {
  active: () => boolean;
  subscribe: (listener: () => void) => () => void;
};

// Keep no timer while hidden and refresh once on resume, without replaying ticks.
export function startActivePolling(
  source: ActivitySource, refresh: () => void, interval: number, pause: () => void = () => {},
) {
  let timer: ReturnType<typeof setInterval> | undefined;
  const update = () => {
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
    if (source.active()) {
      refresh();
      timer = setInterval(() => { if (source.active()) refresh(); }, interval);
    } else pause();
  };
  const unsubscribe = source.subscribe(update);
  update();
  return () => { unsubscribe(); if (timer !== undefined) clearInterval(timer); };
}
