package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Plugins are the features an operator switches on from the panel rather
// than from a config file: a hello bar, a chat widget, an analytics tag, a
// search index, a marketing integration. Each is a descriptor — a key, a
// name, the settings it takes — plus one row that says whether it is on and
// what the settings are. Core ships the descriptors for the storefront
// features that are nothing but settings a storefront reads; a module
// registers its own and reads its settings back at runtime, so a store can
// wire Klaviyo or Meilisearch without an environment variable and without a
// restart.
//
// What a plugin is not: code that arrives at runtime. Everything a plugin
// does is compiled into the binary; the row only decides whether it runs
// and with what. A marketplace of downloadable code is a different product
// with a different security story, and this is deliberately not it.

// PluginDef describes one plugin.
type PluginDef struct {
	// Key is [a-z0-9-]+ and unique. A module's plugin normally shares the
	// module's name.
	Key   string `json:"key"`
	Title string `json:"title"`
	// Icon is a Remix Icon class (ri-…-line) the panel shows beside the title.
	Icon        string `json:"icon"`
	Description string `json:"description"`
	// Category groups the list: storefront, marketing, analytics, chat,
	// search, operations, integration.
	Category string `json:"category"`
	// Fields are the settings the plugin takes, in the order a form shows them.
	Fields []PluginField `json:"fields,omitempty"`
	// Builtin is a descriptor core ships: a feature that is only settings a
	// storefront reads. Module names the module that registered the rest.
	Builtin bool   `json:"builtin"`
	Module  string `json:"module,omitempty"`
	// DefaultEnabled is the state before an operator has touched it.
	DefaultEnabled bool `json:"default_enabled"`
	// Docs is where to read more, when there is somewhere.
	Docs string `json:"docs,omitempty"`
	// Home is the panel screen a built-in feature is set up on, a path
	// under /dash/. A descriptor with a Home is part of the product rather
	// than an add-on: the Plugins screen leaves it out, and its own screen
	// shows its switch and its settings (D132). Nothing else changes — the
	// row, the admin doors, GET /api/plugins and Fill treat it like any
	// plugin, so a store's settings and its storefront are what they were.
	Home string `json:"home,omitempty"`
	// AlwaysOn is a built-in feature with no switch: on with no row, and a
	// patch may not turn it off. A row stored off before the switch went
	// still reads off, so that store's storefront does not change under it,
	// and it can be switched on once, after which it stays on (D132).
	AlwaysOn bool `json:"always_on,omitempty"`
}

// PluginField is one setting.
type PluginField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind is text, textarea, secret, number, bool, select, url or
	// collection. A collection is stored as its handle, as text is; the kind
	// only tells the panel to offer the store's collections to pick from.
	Kind    string   `json:"kind"`
	Help    string   `json:"help,omitempty"`
	Options []string `json:"options,omitempty"`
	// Public settings are handed to the storefront through GET /api/plugins.
	// A secret is never public, whatever this says.
	Public   bool `json:"public"`
	Required bool `json:"required,omitempty"`
	// Default is what an unset field reads as.
	Default any `json:"default,omitempty"`
	// Outbound marks a setting the server itself connects to — a provider's
	// API base URL, a shop's domain — as opposed to one it only hands out,
	// like a storefront link. A new value is checked with
	// App.ValidateOutboundURL as it is saved, so an address the install would
	// refuse to call is refused while the operator is typing it (D109). A url
	// setting holds a URL; any other kind holds a host, with or without a
	// scheme.
	Outbound bool `json:"-"`
}

// Plugin is a descriptor with its state.
type Plugin struct {
	PluginDef
	Enabled bool `json:"enabled"`
	// Settings are the stored values, with every secret masked: a read never
	// hands a key back, and a patch that sends the mask leaves it as it was.
	Settings map[string]any `json:"settings"`
	// Configured is whether every required field has a value — what the list
	// shows beside an enabled plugin that cannot work yet.
	Configured bool `json:"configured"`
	// Unopened names the secrets that are stored but sealed under a key this
	// install does not have. Each reads as the mask, like any stored secret,
	// so the form shows a value is there and an untouched save keeps it; the
	// module reads it as unset, so the plugin is not configured until it is
	// entered again or the original key is put back.
	Unopened  []string   `json:"unopened,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// PluginPatch changes a plugin's state. Settings are merged over what is
// stored; a field absent from the map is left alone.
type PluginPatch struct {
	Enabled  *bool          `json:"enabled"`
	Settings map[string]any `json:"settings"`
}

// PluginPublic is what a storefront is told about an enabled plugin.
type PluginPublic struct {
	Key      string         `json:"key"`
	Title    string         `json:"title"`
	Settings map[string]any `json:"settings"`
}

// SecretMask is what a stored secret reads as, and what a patch sends to
// keep it.
const SecretMask = "••••••••"

var pluginKeyRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var pluginIconRE = regexp.MustCompile(`^ri-[a-z0-9]+(-[a-z0-9]+)*-line$`)

// Plugins is the registry and the state.
type Plugins struct {
	app   *App
	mu    sync.RWMutex
	defs  map[string]PluginDef
	order []string
}

func newPlugins(a *App) *Plugins {
	p := &Plugins{app: a, defs: map[string]PluginDef{}}
	for _, def := range builtinPlugins {
		def.Builtin = true
		if err := p.register(def); err != nil {
			panic("gocommerce: built-in plugin: " + err.Error())
		}
	}
	return p
}

// Plugins returns the plugin registry.
func (a *App) Plugins() *Plugins { return a.plugins }

// RegisterPlugin is how a module declares the plugin it implements, from its
// Register. The module's name is recorded on it, and a key another plugin
// already holds is a registration error.
func (a *App) RegisterPlugin(def PluginDef) {
	def.Builtin = false
	def.Module = a.ownerName()
	if err := a.plugins.register(def); err != nil {
		a.regErrf("module %q: plugin: %v", a.ownerName(), err)
	}
}

func (p *Plugins) register(def PluginDef) error {
	if !pluginKeyRE.MatchString(def.Key) {
		return fmt.Errorf("invalid plugin key %q: want lowercase letters, digits and single dashes", def.Key)
	}
	if strings.TrimSpace(def.Title) == "" {
		return fmt.Errorf("plugin %q has no title", def.Key)
	}
	if !pluginIconRE.MatchString(def.Icon) {
		return fmt.Errorf("plugin %q: icon %q is not a Remix Icon class such as ri-plug-line", def.Key, def.Icon)
	}
	if def.Home != "" && !strings.HasPrefix(def.Home, "/dash/") {
		return fmt.Errorf("plugin %q: home %q is not a panel screen; want a path under /dash/", def.Key, def.Home)
	}
	// A feature with no switch is still reached somewhere, and that screen is
	// the only place it can be switched on again by a store that had it off.
	if def.AlwaysOn && def.Home == "" {
		return fmt.Errorf("plugin %q is always on and names no screen (Home) of its own", def.Key)
	}
	if def.AlwaysOn {
		def.DefaultEnabled = true
	}
	seen := map[string]bool{}
	for _, f := range def.Fields {
		if !pluginKeyRE.MatchString(strings.ReplaceAll(f.Key, "_", "-")) || seen[f.Key] {
			return fmt.Errorf("plugin %q: bad or repeated field key %q", def.Key, f.Key)
		}
		seen[f.Key] = true
		switch f.Kind {
		case "text", "textarea", "secret", "number", "bool", "select", "url", "collection":
		default:
			return fmt.Errorf("plugin %q: field %q has unknown kind %q", def.Key, f.Key, f.Kind)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.defs[def.Key]; ok {
		return fmt.Errorf("plugin %q is already registered", def.Key)
	}
	p.defs[def.Key] = def
	p.order = append(p.order, def.Key)
	return nil
}

// Defs is every descriptor, built-ins first, in registration order.
func (p *Plugins) Defs() []PluginDef {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PluginDef, 0, len(p.order))
	for _, key := range p.order {
		out = append(out, p.defs[key])
	}
	return out
}

func (p *Plugins) def(key string) (PluginDef, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	def, ok := p.defs[key]
	return def, ok
}

// pluginRow is the stored state, or its absence.
type pluginRow struct {
	enabled bool
	// settings has every secret opened, and one that would not open left out.
	settings map[string]any
	// unopened are the secrets that would not open, exactly as stored. They
	// read as unset, and they are written back as they were until a save
	// names the field — a new value, or an empty one to clear it: a save that
	// left the field out, or sent the mask, must not destroy what putting the
	// right key back would still open.
	unopened map[string]any
	// sealed are the fields that were stored sealed and opened. They are
	// sealed again on write whatever the descriptor now says: a release that
	// drops a secret field, or makes it text, must not have the next save
	// write the credential out in the clear.
	sealed    map[string]bool
	updatedAt *time.Time
	present   bool
}

func (p *Plugins) row(ctx context.Context, q rowQuerier, key string) (pluginRow, error) {
	var r pluginRow
	var raw []byte
	var at time.Time
	err := q.QueryRowContext(ctx,
		`SELECT enabled, settings, updated_at FROM plugins WHERE key = $1`, key).Scan(&r.enabled, &raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, Internalf(err, "read plugin")
	}
	r.present = true
	r.updatedAt = &at
	if err := json.Unmarshal(raw, &r.settings); err != nil {
		return r, Internalf(err, "decode plugin settings")
	}
	p.openSettings(ctx, key, &r)
	return r, nil
}

// pluginSecretField is the label a plugin's secret is sealed under.
func pluginSecretField(plugin, field string) string { return "plugins/" + plugin + "/" + field }

// openSettings opens every sealed value in a row. It goes by the envelope
// rather than by the field's kind, so a row is read the same whether or not
// this binary has the plugin's module installed.
func (p *Plugins) openSettings(ctx context.Context, key string, r *pluginRow) {
	for k, v := range r.settings {
		s, ok := v.(string)
		if !ok || !isSealed(s) {
			continue
		}
		plain, ok := p.app.openSecretOrUnset(ctx, pluginSecretField(key, k), s)
		if !ok {
			if r.unopened == nil {
				r.unopened = map[string]any{}
			}
			r.unopened[k] = s
			delete(r.settings, k)
			continue
		}
		if r.sealed == nil {
			r.sealed = map[string]bool{}
		}
		r.sealed[k] = true
		r.settings[k] = plain
	}
}

// sealSettings is what a row stores: every secret sealed when the install has
// a key — a field of kind secret, and any field whose value was stored sealed
// — and the secrets that would not open and this save kept, as they were.
func (p *Plugins) sealSettings(ctx context.Context, def PluginDef, settings map[string]any, wasSealed map[string]bool, kept map[string]any) (map[string]any, error) {
	secret := map[string]bool{}
	for _, f := range def.Fields {
		secret[f.Key] = f.Kind == "secret"
	}
	for k := range wasSealed {
		secret[k] = true
	}
	out := make(map[string]any, len(settings)+len(kept))
	for k, v := range settings {
		if s, ok := v.(string); ok && secret[k] {
			sealed, err := p.app.SealSecret(ctx, pluginSecretField(def.Key, k), s)
			if err != nil {
				return nil, err
			}
			v = sealed
		}
		out[k] = v
	}
	for k, v := range kept {
		out[k] = v
	}
	return out, nil
}

// assemble joins a descriptor with its row, masking secrets.
func assemble(def PluginDef, r pluginRow) *Plugin {
	out := &Plugin{PluginDef: def, Enabled: def.DefaultEnabled, Settings: map[string]any{}, UpdatedAt: r.updatedAt}
	if r.present {
		out.Enabled = r.enabled
	}
	out.Configured = true
	for _, f := range def.Fields {
		v, ok := r.settings[f.Key]
		if !ok || v == nil || v == "" {
			// Stored, but sealed under a key this install does not have. The
			// panel is told a value is there — the mask, as for any secret, so
			// a form saved untouched keeps it — and which field it is; the
			// module reads it as unset, so the plugin is not configured.
			if _, stored := r.unopened[f.Key]; stored {
				out.Unopened = append(out.Unopened, f.Key)
				if f.Kind == "secret" {
					out.Settings[f.Key] = SecretMask
				}
				if f.Required {
					out.Configured = false
				}
				continue
			}
			if f.Default != nil {
				out.Settings[f.Key] = f.Default
			}
			if f.Required {
				out.Configured = false
			}
			continue
		}
		if f.Kind == "secret" {
			out.Settings[f.Key] = SecretMask
			continue
		}
		out.Settings[f.Key] = v
	}
	return out
}

// List is every plugin with its state.
func (p *Plugins) List(ctx context.Context) ([]*Plugin, error) {
	rows, err := p.app.db.QueryContext(ctx, `SELECT key, enabled, settings, updated_at FROM plugins`)
	if err != nil {
		return nil, Internalf(err, "list plugins")
	}
	defer rows.Close()
	stored := map[string]pluginRow{}
	for rows.Next() {
		var key string
		var r pluginRow
		var raw []byte
		var at time.Time
		if err := rows.Scan(&key, &r.enabled, &raw, &at); err != nil {
			return nil, Internalf(err, "scan plugin")
		}
		r.present, r.updatedAt = true, &at
		_ = json.Unmarshal(raw, &r.settings)
		p.openSettings(ctx, key, &r)
		stored[key] = r
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "list plugins")
	}
	var out []*Plugin
	for _, def := range p.Defs() {
		out = append(out, assemble(def, stored[def.Key]))
	}
	return out, nil
}

// Get is one plugin with its state.
func (p *Plugins) Get(ctx context.Context, key string) (*Plugin, error) {
	def, ok := p.def(key)
	if !ok {
		return nil, NotFoundf("no plugin is called %q", key)
	}
	r, err := p.row(ctx, p.app.db, key)
	if err != nil {
		return nil, err
	}
	return assemble(def, r), nil
}

// Update switches a plugin on or off and changes its settings, in one
// transaction, audited.
func (p *Plugins) Update(ctx context.Context, key string, patch PluginPatch) (*Plugin, error) {
	def, ok := p.def(key)
	if !ok {
		return nil, NotFoundf("no plugin is called %q", key)
	}
	fields := map[string]PluginField{}
	for _, f := range def.Fields {
		fields[f.Key] = f
	}
	for k := range patch.Settings {
		if _, ok := fields[k]; !ok {
			return nil, Validationf("plugin %q has no setting %q", key, k)
		}
	}

	var out *Plugin
	err := InTx(ctx, p.app.db, func(tx *sql.Tx) error {
		current, err := p.row(ctx, tx, key)
		if err != nil {
			return err
		}
		settings := map[string]any{}
		// cleared are the fields this patch empties on purpose, which clears
		// them even when what is stored would not open.
		cleared := map[string]bool{}
		for k, v := range current.settings {
			settings[k] = v
		}
		for k, v := range patch.Settings {
			f := fields[k]
			clean, err := coercePluginValue(f, v)
			if err != nil {
				return err
			}
			// The mask means "as it was": a form that read the plugin back
			// and saved it unchanged must not overwrite the key with dots.
			if f.Kind == "secret" && clean == SecretMask {
				continue
			}
			if clean == nil || clean == "" {
				delete(settings, k)
				cleared[k] = true
				continue
			}
			// Only a changed value is judged: a form that saves every field
			// must not be refused over an address it did not touch. One the
			// switch let in earlier is refused where it is used instead.
			if s, ok := clean.(string); ok && f.Outbound && clean != current.settings[k] {
				// Any other kind holds a host, typed with or without a scheme
				// and maybe a path after it: a shop domain, copied from the
				// address bar.
				if f.Kind != "url" && !strings.Contains(s, "://") {
					s = "https://" + s
				}
				if problem := p.app.outboundURLProblem(s); problem != "" {
					return Validationf("Enter %s as an address on the public internet: %s.", f.Label, problem)
				}
			}
			settings[k] = clean
		}
		enabled := current.enabled
		if !current.present {
			enabled = def.DefaultEnabled
		}
		wasOn := current.enabled || (!current.present && def.DefaultEnabled)
		if patch.Enabled != nil {
			if def.AlwaysOn && wasOn && !*patch.Enabled {
				return Validationf("%s is always on: it is part of the store, not an add-on to switch off.", def.Title)
			}
			enabled = *patch.Enabled
		}
		// A plugin that is on must have what it needs to work. Checked when
		// it is switched on and when this patch empties a required setting —
		// not on every save, so a plugin that was on before this rule can
		// still have its other settings changed.
		// The secrets that would not open and stay as they are: this save
		// neither typed a new value nor emptied the field.
		kept := map[string]any{}
		for k, v := range current.unopened {
			if _, replaced := settings[k]; !replaced && !cleared[k] {
				kept[k] = v
			}
		}
		if enabled {
			var missing []PluginField
			for _, f := range def.Fields {
				if !f.Required {
					continue
				}
				if v := settings[f.Key]; v != nil && v != "" {
					continue
				}
				// A plugin already on keeps an unreadable key it was not asked
				// to change, so an untouched save of its form still saves;
				// switching one on still needs a key that opens.
				if _, ok := kept[f.Key]; ok && wasOn {
					continue
				}
				if _, emptied := patch.Settings[f.Key]; !wasOn || emptied {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				return requiredSettingsError(def, missing, wasOn)
			}
		}
		stored, err := p.sealSettings(ctx, def, settings, current.sealed, kept)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(stored)
		if err != nil {
			return Internalf(err, "encode plugin settings")
		}
		var at time.Time
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO plugins (key, enabled, settings, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (store_id, key) DO UPDATE SET enabled = EXCLUDED.enabled, settings = EXCLUDED.settings, updated_at = now()
			RETURNING updated_at`, key, enabled, encoded).Scan(&at); err != nil {
			return Internalf(err, "write plugin")
		}
		out = assemble(def, pluginRow{enabled: enabled, settings: settings, unopened: kept, updatedAt: &at, present: true})

		summary := "Changed " + def.Title + "'s settings"
		switch {
		case patch.Enabled != nil && *patch.Enabled && (!current.present || !current.enabled):
			summary = "Enabled " + def.Title
		case patch.Enabled != nil && !*patch.Enabled && (current.present && current.enabled || !current.present && def.DefaultEnabled):
			summary = "Disabled " + def.Title
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditPluginUpdate, Entity: AuditEntityPlugin, Key: key, Label: def.Title,
			Summary: summary,
			After:   map[string]any{"enabled": enabled, "settings": out.Settings},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// requiredSettingsError says which settings a plugin needs before it can be
// on, and what to put in each — the next thing to do, not what went wrong.
func requiredSettingsError(def PluginDef, missing []PluginField, wasOn bool) error {
	parts := make([]string, 0, len(missing))
	for _, f := range missing {
		parts = append(parts, f.Label+fieldHint(f, def))
	}
	list := strings.Join(parts, "; ")
	if wasOn {
		return Validationf("%s is on and needs %s. Enter it, or switch %s off first.", def.Title, list, def.Title)
	}
	return Validationf("Before switching %s on, fill in %s.", def.Title, list)
}

// fieldHint is what a setting's value looks like, said in a few words.
func fieldHint(f PluginField, def PluginDef) string {
	switch f.Kind {
	case "url":
		return " (the full address, starting with https://, for example https://shop.example.com)"
	case "secret":
		return " (from your " + def.Title + " account)"
	case "number":
		return " (a number)"
	case "select":
		if len(f.Options) > 0 {
			return " (one of " + strings.Join(f.Options, ", ") + ")"
		}
	}
	return ""
}

// coercePluginValue turns what a form or a client sent into the field's own
// type, or refuses it by saying what to enter instead.
func coercePluginValue(f PluginField, v any) (any, error) {
	switch f.Kind {
	case "bool":
		switch b := v.(type) {
		case bool:
			return b, nil
		case string:
			switch strings.ToLower(b) {
			case "true", "1", "yes", "on":
				return true, nil
			case "false", "0", "no", "off", "":
				return false, nil
			}
		case nil:
			return nil, nil
		}
		return nil, Validationf("Set %s to on or off.", f.Label)
	case "number":
		switch n := v.(type) {
		case float64:
			return n, nil
		case int:
			return float64(n), nil
		case int64:
			return float64(n), nil
		case json.Number:
			x, err := n.Float64()
			if err != nil {
				return nil, Validationf("Enter %s as a number, for example 10.", f.Label)
			}
			return x, nil
		case string:
			if strings.TrimSpace(n) == "" {
				return nil, nil
			}
			var x float64
			if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &x); err != nil {
				return nil, Validationf("Enter %s as a number, for example 10.", f.Label)
			}
			return x, nil
		case nil:
			return nil, nil
		}
		return nil, Validationf("Enter %s as a number, for example 10.", f.Label)
	case "select":
		s, ok := v.(string)
		if v == nil || (ok && s == "") {
			return nil, nil
		}
		if !ok {
			return nil, Validationf("Choose %s from the list: %s.", f.Label, strings.Join(f.Options, ", "))
		}
		for _, o := range f.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, Validationf("Choose %s from the list: %s.", f.Label, strings.Join(f.Options, ", "))
	case "url":
		s, ok := v.(string)
		if v == nil || (ok && strings.TrimSpace(s) == "") {
			return nil, nil
		}
		if !ok || !(strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
			return nil, Validationf("Enter %s as a full web address starting with https://, for example https://shop.example.com.", f.Label)
		}
		return strings.TrimSpace(s), nil
	default:
		if v == nil {
			return nil, nil
		}
		s, ok := v.(string)
		if !ok {
			return nil, Validationf("Enter %s as text.", f.Label)
		}
		s = strings.TrimSpace(s)
		// Every setting is read through the same door, and a value with this
		// prefix would be taken for one the engine sealed, fail to open and
		// read as unset. No real key starts this way, so it is a paste of
		// the wrong thing; the value itself is not repeated back.
		if isSealed(s) {
			return nil, Validationf("%s cannot start with %q, which marks a value the engine encrypted. Paste the value itself.",
				f.Label, sealedPrefix)
		}
		return s, nil
	}
}

// Enabled is whether a plugin is switched on — what a module asks before it
// does anything. An unknown key is off.
func (p *Plugins) Enabled(ctx context.Context, key string) (bool, error) {
	return p.enabled(ctx, p.app.db, key)
}

// EnabledTx is Enabled read through tx, for a module that decides inside a
// transaction: a second connection taken from the pool while tx holds one
// can wait on the very pool tx fills.
func (p *Plugins) EnabledTx(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	return p.enabled(ctx, tx, key)
}

func (p *Plugins) enabled(ctx context.Context, q rowQuerier, key string) (bool, error) {
	def, ok := p.def(key)
	if !ok {
		return false, nil
	}
	r, err := p.row(ctx, q, key)
	if err != nil {
		return false, err
	}
	if !r.present {
		return def.DefaultEnabled, nil
	}
	return r.enabled, nil
}

// Settings are the stored values, unmasked, with defaults filled in — for
// the module that owns the plugin, never for a client.
func (p *Plugins) Settings(ctx context.Context, key string) (map[string]any, error) {
	def, ok := p.def(key)
	if !ok {
		return nil, NotFoundf("no plugin is called %q", key)
	}
	r, err := p.row(ctx, p.app.db, key)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, f := range def.Fields {
		if v, ok := r.settings[f.Key]; ok && v != nil && v != "" {
			out[f.Key] = v
		} else if f.Default != nil {
			out[f.Key] = f.Default
		}
	}
	return out, nil
}

// Unopened names the plugin's settings that are stored sealed and will not
// open — sealed under a key the install no longer has. Settings, String and
// Fill read them as unset, which is right for a required key (the plugin is
// not configured) and wrong for an optional one that switches a check on: a
// module with such a field asks here, and refuses rather than run with the
// check quietly off.
func (p *Plugins) Unopened(ctx context.Context, key string) ([]string, error) {
	if _, ok := p.def(key); !ok {
		return nil, NotFoundf("no plugin is called %q", key)
	}
	r, err := p.row(ctx, p.app.db, key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(r.unopened))
	for k := range r.unopened {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// Fill copies a plugin's stored settings onto a module's Config, field by
// field, wherever the operator typed a value: a struct field tagged
// `plugin:"api_key"` takes the setting called api_key. Config's own value
// stands where nothing was typed, and a field's Default applies where both
// are empty. Nested structs are walked; pointers, slices and maps are left
// alone. A string field takes text, secret, select, url or textarea; a bool
// field takes bool; an integer or float field takes number, or numeric text.
//
// This is the whole of what a provider module needs to be set up from the
// panel: tag its Config, declare the fields, call Fill before each use.
func (p *Plugins) Fill(ctx context.Context, key string, dst any) error {
	def, ok := p.def(key)
	if !ok {
		return NotFoundf("no plugin is called %q", key)
	}
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("plugins: Fill wants a pointer to a struct, got %T", dst)
	}
	r, err := p.row(ctx, p.app.db, key)
	if err != nil {
		return err
	}
	defaults := map[string]any{}
	for _, f := range def.Fields {
		if f.Default != nil {
			defaults[f.Key] = f.Default
		}
	}
	return fillStruct(rv.Elem(), r.settings, defaults)
}

func fillStruct(v reflect.Value, settings, defaults map[string]any) error {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field, fv := t.Field(i), v.Field(i)
		if !fv.CanSet() {
			continue
		}
		key := field.Tag.Get("plugin")
		if key == "" {
			if fv.Kind() == reflect.Struct {
				if err := fillStruct(fv, settings, defaults); err != nil {
					return err
				}
			}
			continue
		}
		val, present := settings[key]
		if !present || val == nil || val == "" {
			d, has := defaults[key]
			if !has || !fv.IsZero() {
				continue
			}
			val = d
		}
		if err := assignSetting(fv, val); err != nil {
			return Validationf("plugin setting %q: %v", key, err)
		}
	}
	return nil
}

func assignSetting(fv reflect.Value, val any) error {
	switch fv.Kind() {
	case reflect.String:
		s, ok := val.(string)
		if !ok {
			s = fmt.Sprint(val)
		}
		fv.SetString(strings.TrimSpace(s))
	case reflect.Bool:
		switch b := val.(type) {
		case bool:
			fv.SetBool(b)
		case string:
			fv.SetBool(b == "true" || b == "1" || b == "yes" || b == "on")
		default:
			return fmt.Errorf("want true or false, got %T", val)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch n := val.(type) {
		case float64:
			fv.SetInt(int64(n))
		case int:
			fv.SetInt(int64(n))
		case int64:
			fv.SetInt(n)
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil {
				return fmt.Errorf("want a number, got %q", n)
			}
			fv.SetInt(int64(parsed))
		default:
			return fmt.Errorf("want a number, got %T", val)
		}
	case reflect.Float32, reflect.Float64:
		switch n := val.(type) {
		case float64:
			fv.SetFloat(n)
		case int:
			fv.SetFloat(float64(n))
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil {
				return fmt.Errorf("want a number, got %q", n)
			}
			fv.SetFloat(parsed)
		default:
			return fmt.Errorf("want a number, got %T", val)
		}
	default:
		return fmt.Errorf("a %s field cannot be set from the panel", fv.Kind())
	}
	return nil
}

// String is one setting as text, "" when unset; the shape most modules want.
func (p *Plugins) String(ctx context.Context, key, field string) string {
	settings, err := p.Settings(ctx, key)
	if err != nil {
		return ""
	}
	switch v := settings[field].(type) {
	case string:
		return v
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

// Public is what a storefront is told: every enabled plugin and its public
// settings, nothing else.
func (p *Plugins) Public(ctx context.Context) ([]PluginPublic, error) {
	all, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []PluginPublic{}
	for _, pl := range all {
		if !pl.Enabled {
			continue
		}
		pub := PluginPublic{Key: pl.Key, Title: pl.Title, Settings: map[string]any{}}
		for _, f := range pl.Fields {
			if f.Public && f.Kind != "secret" {
				if v, ok := pl.Settings[f.Key]; ok {
					pub.Settings[f.Key] = v
				}
			}
		}
		out = append(out, pub)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
