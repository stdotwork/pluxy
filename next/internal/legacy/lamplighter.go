package legacy

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The Lamplighter plugin for today's core (github.com/PosvdM/cpa-plugin-lamplighter) watches
// accounts' usage windows, sends notifications about them, and starts an account's next 5-hour
// window right after it resets (window ignition). pluxy has window ignition built in
// (routing.ignition); the plugin's notifications and its own page have no counterpart. lamplighter
// maps the plugin's settings, under plugins.configs.lamplighter, to routing.ignition, and gives
// each of its keys a row: the plugin's keys and defaults are read as the plugin reads them.

const lampPath = "plugins.configs.lamplighter"

// lampOffWhy is why the plugin's settings are not brought while it is off.
const lampOffWhy = "the Lamplighter plugin's settings; its window ignition is routing.ignition in pluxy"

// Extension IDs of the plugin's providers that start windows. Antigravity's extension is
// archived: it serves only once it is installed by hand.
const lampAntigravity = "pluxy/antigravity"

var lampProviders = []struct {
	id, ext string
	ignite  bool // the plugin's default
}{{"claude", Claude, true}, {"codex", Codex, true}, {"antigravity", lampAntigravity, false}}

// lampNoIgnition are the plugin's providers it starts no windows for, with its other names
// for them.
var lampNoIgnition = map[string]string{"kimi": "kimi", "kimi-ai": "kimi", "kimi.ai": "kimi", "kimi.com": "kimi",
	"xai": "xai", "x-ai": "xai", "x.ai": "xai", "grok": "xai"}

// lampGroups are the plugin's names for Antigravity's model groups, as it reads them from
// Antigravity's usage reply, and the window ID the antigravity extension gives each group's
// five-hour window.
var lampGroups = map[string]string{"gemini": "gemini_models_five_hour", "claude / gpt": "claude_and_gpt_models_five_hour"}

// lampDefaultGroups is the groups the plugin starts when its antigravity block names none.
var lampDefaultGroups = []string{"Gemini"}

var lampNotAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// lampWindow returns the window ID of the Antigravity group the plugin names label: a known
// group's, else the one the extension makes of a group of that name.
func lampWindow(label string) string {
	l := lampLabel(label)
	if w, ok := lampGroups[l]; ok {
		return w
	}
	return strings.Trim(lampNotAlnum.ReplaceAllString(l, "_"), "_") + "_five_hour"
}

// The plugin's settings that pluxy has no counterpart for, by key, with why.
var (
	lampNotify   = "pluxy sends no usage notifications or reminders; the panel shows each account's usage, and pluxy account list its health"
	lampDropped  = map[string]string{}
	lampDropKeys = map[string][]string{
		lampNotify: {"bark_url", "bark_group", "bark_icon", "webhook", "notice_threshold", "low_threshold", "critical_threshold",
			"recovery_notify", "reset_reminder", "notify_recovery", "notify_reset_reminders", "codex_reset_updates"},
		"pluxy reads routing.ignition's hours in the machine's local time zone; move start_hour and end_hour, or set the machine's time zone": {
			"timezone", "timezone_offset_hours"},
		"pluxy reads an account's usage when one of its windows is due to reset, and an hour after a reading with nothing to wait for, not on a fixed interval": {
			"poll_interval_seconds"},
		"an ignition request takes at most 2 minutes in pluxy, and a usage reading at most 30 seconds": {"request_timeout_seconds"},
		"pluxy reads reset times from the provider's usage reading, not from replies to requests":      {"passive_skip_seconds", "passive_skip_max_minutes"},
		"core sends the ignition request to the account itself, with no client key or address":         {"cpa_base_url", "models_api_key"},
		"pluxy has no MCP server for usage": {"mcp_enabled", "mcp_listen", "mcp_key"},
		"the plugin's own page and records; pluxy keeps its usage record and shows it in the panel": {
			"config_token", "data_dir", "history_retention_days", "remember_chart_filters", "remember_chart_expanded", "account_weights", "account_order"},
	}
)

func init() {
	for why, keys := range lampDropKeys {
		for _, k := range keys {
			lampDropped[k] = why
		}
	}
}

// lampNumber reads a number the plugin reads: a YAML number, or text holding one.
func lampNumber(v any) (float64, bool) {
	switch v := value(v).(type) {
	case int:
		return float64(v), true
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// lampSetting is one of the plugin's ignition settings: its key, the key in routing.ignition,
// the factor from the plugin's unit to pluxy's, the plugin's default and the range it holds the
// value to.
type lampSetting struct {
	key, to   string
	factor    float64
	def       float64
	low, high float64
	whole     bool
}

var lampIgnition = []lampSetting{
	{"start_hour", "start_hour", 1, 7, 0, 23, true},
	{"end_hour", "end_hour", 1, 22, 0, 23, true},
	{"end_grace_minutes", "end_grace_ms", 60000, 30, 0, 24 * 60, true},
	{"grace_seconds", "grace_ms", 1000, 3, 0, 24 * 3600, true},
	{"failure_retry_seconds", "retry_ms", 1000, 300, 60, 24 * 3600, true},
	{"failure_backoff_multiplier", "backoff_multiplier", 1, 3, 1, 10, false},
	{"max_transient_failures", "max_failures", 1, 3, 1, 10, true},
	{"post_success_hold_seconds", "hold_ms", 1000, 60, 15, 24 * 3600, true},
}

// lamplighter brings the plugin's window ignition to routing.ignition when the plugin was on
// (plugins.enabled and its own enabled both true, as today's core and the plugin need), and
// gives each of its keys a row. While it is off its settings are left to the rules, which name
// them as not brought.
func (x *translator) lamplighter() {
	v, ok := get(x.t, lampPath)
	lt, isMap := v.(tree)
	if !ok || !isMap {
		return
	}
	pluginsOn, _ := flag(x.t, "plugins.enabled")
	on, _ := boolean_(lt["enabled"], lampPath+".enabled")
	if !on {
		// The plugin was off: none of its settings did anything.
		lampLeaves(lampPath, lt, func(p string, _ any) {
			x.mark(NotBrought, p, Row{Why: lampOffWhy + "; the plugin is off here, so nothing is lost"})
		})
		del(x.t, lampPath)
		return
	}
	if !pluginsOn {
		// Its own switch is on, but plugins.enabled keeps every plugin off: the plugin's settings
		// are named with the rest of plugins, as off.
		return
	}
	x.notes = append(x.notes, "config.yaml: "+lampPath+": the Lamplighter plugin's window ignition runs as routing.ignition; its notifications are not supported")
	rows := map[string]Entry{}
	row := func(rest string, list int, r Row) { rows[rest] = Entry{List: list, Row: r} }

	ign := map[string]any{"enabled": true}
	it, _ := lt["ignition"].(tree)
	if e, has := it["enabled"]; has {
		on, err := boolean_(e, "")
		switch {
		case err != nil:
			row("ignition.enabled", NotBrought, Row{Why: "not true or false; the plugin's default, on, applies"})
		default:
			ign["enabled"] = on
			row("ignition.enabled", Brought, Row{As: "routing.ignition.enabled"})
		}
	}
	for _, s := range lampIgnition {
		raw, has := it[s.key]
		if !has {
			continue
		}
		rest := "ignition." + s.key
		n, isNum := lampNumber(raw)
		if !isNum {
			row(rest, NotBrought, Row{Why: fmt.Sprintf("not a number; the plugin's default, %v, applies here too", s.def)})
			continue
		}
		held := math.Min(s.high, math.Max(s.low, n))
		if s.whole {
			held = math.Trunc(held)
		}
		if s.factor == 1 && !s.whole {
			ign[s.to] = held
		} else {
			ign[s.to] = int64(held * s.factor)
		}
		as := "routing.ignition." + s.to
		if held != n {
			row(rest, Changed, Row{From: strconv.FormatFloat(n, 'g', -1, 64), To: fmt.Sprintf("%s %v", as, ign[s.to]),
				Why: fmt.Sprintf("the plugin holds it to %v-%v, so it used %v", s.low, s.high, held)})
		} else {
			row(rest, Brought, Row{As: as})
		}
	}

	provs, _ := lt["providers"].(tree)
	exts := map[string]any{}
	var names []string
	for _, p := range lampProviders {
		var block tree
		key := p.id
		for k, b := range provs {
			if strings.ToLower(strings.TrimSpace(k)) == p.id {
				block, _ = b.(tree)
				key = k
			}
		}
		base := "providers." + key + "."
		as := "routing.ignition.extensions." + p.ext
		monitor, ignite := true, p.ignite
		for _, sw := range []struct {
			k string
			v *bool
		}{{"monitor", &monitor}, {"ignite", &ignite}} {
			if raw, has := block[sw.k]; has {
				if b, err := boolean_(raw, ""); err == nil {
					*sw.v = b
				} else {
					row(base+sw.k, NotBrought, Row{Why: "not true or false; the plugin's default applies"})
				}
			}
		}
		on := monitor && ignite
		model := ""
		if raw, has := block["model"]; has {
			m, err := scalar(raw, "")
			model = strings.TrimSpace(m)
			switch {
			case err != nil:
				row(base+"model", NotBrought, Row{Why: "not a model name; a small model the account serves is used"})
			case !on:
				row(base+"model", NotBrought, Row{Why: "the plugin starts no windows for this provider here, so nothing is lost"})
			case model == "":
				row(base+"model", Brought, Row{As: as + ", with no model: a small model the account serves"})
			default:
				row(base+"model", Brought, Row{As: as + ".model"})
			}
		}
		// Said, though an entry is on without it: an import writes a mapping field by field, so an
		// empty entry would not reach the config file.
		ext := map[string]any{"enabled": true}
		if model != "" {
			ext["model"] = model
		}
		archived := ""
		if p.ext == lampAntigravity {
			archived = "pluxy/antigravity is an archived extension that serves only once it is installed by hand; each group the plugin starts is that extension's five-hour window of the group (Gemini is gemini_models_five_hour, Claude / GPT claude_and_gpt_models_five_hour)"
			groups := lampDefaultGroups
			if raw, has := block["groups"]; has {
				list, err := stringList(raw, "")
				switch {
				case err != nil:
					row(base+"groups", NotBrought, Row{Why: "not a list of group names; the plugin's default, Gemini, applies"})
				case !on:
					row(base+"groups", NotBrought, Row{Why: "the plugin starts no windows for this provider here, so nothing is lost"})
				default:
					groups = list
					row(base+"groups", Changed, Row{As: as + ".windows", Why: archived})
				}
			}
			models := map[string]string{}
			mt, _ := block["models"].(tree)
			for _, label := range slices.Sorted(maps.Keys(mt)) {
				m, err := scalar(mt[label], "")
				rest := base + "models." + label
				switch {
				case err != nil:
					row(rest, NotBrought, Row{Why: "not a model name"})
				case !on:
					row(rest, NotBrought, Row{Why: "the plugin starts no windows for this provider here, so nothing is lost"})
				default:
					models[lampLabel(label)] = strings.TrimSpace(m)
					row(rest, Brought, Row{As: as + ".windows." + lampWindow(label)})
				}
			}
			if len(groups) == 0 {
				// An empty list starts every group: the two Antigravity has, and any a model is set for.
				groups = []string{"Gemini", "Claude / GPT"}
				for _, label := range slices.Sorted(maps.Keys(mt)) {
					groups = append(groups, label)
				}
			}
			windows := map[string]any{}
			for _, g := range groups {
				windows[lampWindow(g)] = models[lampLabel(g)]
			}
			ext["windows"] = windows
		} else {
			for _, k := range []string{"groups", "models"} {
				if _, has := block[k]; has {
					leaves(base+k, block[k], func(q string) {
						row(q, NotBrought, Row{Why: "the plugin reads " + k + " for antigravity only"})
					})
				}
			}
		}
		if _, has := block["monitor"]; has && rows[base+"monitor"].Row.Why == "" {
			if monitor {
				row(base+"monitor", NotBrought, Row{Why: "pluxy reads usage by itself, for ignition and routing; it is at the plugin's default here, so nothing is lost"})
			} else {
				row(base+"monitor", Changed, Row{As: "no " + as, Why: "monitor: false also kept the plugin from starting this provider's windows, so pluxy does not start them either"})
			}
		}
		if _, has := block["ignite"]; has && rows[base+"ignite"].Row.Why == "" {
			switch {
			case !on:
				row(base+"ignite", Brought, Row{As: "no " + as})
			case archived != "":
				row(base+"ignite", Changed, Row{As: as, Why: archived})
			default:
				row(base+"ignite", Brought, Row{As: as})
			}
		}
		if on {
			exts[p.ext] = ext
			names = append(names, p.ext)
		}
	}
	if len(exts) > 0 {
		ign["extensions"] = exts
	}
	slices.Sort(names)
	who := "no provider's accounts"
	if len(names) > 0 {
		who = "the accounts of " + strings.Join(names, ", ")
	}
	row("enabled", Changed, Row{From: "the Lamplighter plugin", To: "routing.ignition, built into core",
		Why: "window ignition is part of pluxy, so the plugin is not needed; its ignition settings become routing.ignition, for " + who +
			"; the hours are the machine's local time"})

	lampLeaves(lampPath, lt, func(p string, v any) {
		rest := strings.TrimPrefix(p, lampPath+".")
		if e, ok := rows[rest]; ok {
			x.mark(e.List, p, e.Row)
			return
		}
		top, _, _ := strings.Cut(rest, ".")
		if why, ok := lampDropped[top]; ok {
			if asksNothing(p, v) {
				why += "; it is off, empty or at the plugin's default here, so nothing is lost"
			}
			x.mark(NotBrought, p, Row{Why: why})
			return
		}
		if top == "providers" {
			pid, _, _ := strings.Cut(strings.TrimPrefix(rest, "providers."), ".")
			if _, ok := lampNoIgnition[strings.ToLower(strings.TrimSpace(pid))]; ok {
				x.mark(NotBrought, p, Row{Why: "the plugin starts no windows for this provider either, and pluxy has no usage notifications, so nothing is lost"})
				return
			}
		}
		x.mark(NotBrought, p, Row{Why: "not a key the Lamplighter plugin or pluxy reads"})
	})
	del(x.t, lampPath)
	r, _ := x.out["routing"].(map[string]any)
	if r == nil {
		r = map[string]any{}
		x.out["routing"] = r
	}
	r["ignition"] = ign
}

// lampLabel is an Antigravity group's name as the plugin compares it: trimmed, in lower case,
// with its spaces made one.
func lampLabel(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// lampLeaves calls f with the path and value of each item of the report below path, as leaves
// names them (a mapping's keys, and a list as one item); the value is passed along, as a key of
// the plugin's settings may hold a dot (a credential file's name) that get cannot follow.
func lampLeaves(path string, v any, f func(string, any)) {
	if t, ok := v.(tree); ok && len(t) > 0 {
		for _, k := range slices.Sorted(maps.Keys(t)) {
			lampLeaves(path+"."+k, t[k], f)
		}
		return
	}
	f(path, v)
}
