import { useEffect, useState } from "preact/hooks";
import { Button, Card, Notice, Switch } from "../../ui/kit/ui";
import { type StartupPreferences, startupGet, startupSet } from "./host";

export function StartupSettings() {
  const [preferences, setPreferences] = useState<StartupPreferences>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    let live = true;
    startupGet().then(
      (p) => {
        if (live) setPreferences(p);
      },
      (e) => {
        if (live) setError(String(e));
      },
    );
    return () => {
      live = false;
    };
  }, []);
  function change(key: keyof StartupPreferences, value: boolean) {
    if (preferences) setPreferences({ ...preferences, [key]: value });
    setSaved(false);
  }
  async function save() {
    if (!preferences) return;
    setBusy(true);
    setError(undefined);
    setSaved(false);
    try {
      setPreferences(await startupSet(preferences));
      setSaved(true);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card title="Startup">
      <form
        aria-label="Startup preferences"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <fieldset disabled={busy || !preferences}>
          <Switch
            label="Launch at startup"
            hint="Start pluxy when you sign in to Linux. Login launches always use the tray."
            checked={preferences?.autostart_enabled ?? false}
            onChange={(v) => change("autostart_enabled", v)}
          />
          <Switch
            label="Silent Launch"
            hint="Start without a window and run in the system tray."
            checked={preferences?.silent_start_enabled ?? false}
            onChange={(v) => change("silent_start_enabled", v)}
          />
          <Switch
            label="Start Core on Launch"
            hint="Start the app's managed core. Local and remote cores are left running as they are."
            checked={preferences?.start_core_on_launch ?? true}
            onChange={(v) => change("start_core_on_launch", v)}
          />
          <Button type="submit" variant="primary">
            {busy ? "Saving…" : "Save startup preferences"}
          </Button>
        </fieldset>
        {error && <Notice tone="danger">{error}</Notice>}
        {saved && (
          <Notice tone="ok">
            Saved. Login registration is {preferences?.autostart_enabled ? "enabled" : "disabled"}. The other
            choices apply the next time the app starts.
          </Notice>
        )}
      </form>
    </Card>
  );
}
