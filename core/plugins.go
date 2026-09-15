package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
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
	Key         string `json:"key"`
	Title       string `json:"title"`
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
}

// PluginField is one setting.
type PluginField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind is text, textarea, secret, number, bool, select or url.
	Kind    string   `json:"kind"`
	Help    string   `json:"help,omitempty"`
	Options []string `json:"options,omitempty"`
	// Public settings are handed to the storefront through GET /api/plugins.
	// A secret is never public, whatever this says.
	Public   bool `json:"public"`
	Required bool `json:"required,omitempty"`
	// Default is what an unset field reads as.
	Default any `json:"default,omitempty"`
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
	Configured bool       `json:"configured"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
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
	seen := map[string]bool{}
	for _, f := range def.Fields {
		if !pluginKeyRE.MatchString(strings.ReplaceAll(f.Key, "_", "-")) || seen[f.Key] {
			return fmt.Errorf("plugin %q: bad or repeated field key %q", def.Key, f.Key)
		}
		seen[f.Key] = true
		switch f.Kind {
		case "text", "textarea", "secret", "number", "bool", "select", "url":
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
	enabled   bool
	settings  map[string]any
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
	return r, nil
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
				continue
			}
			settings[k] = clean
		}
		enabled := current.enabled
		if !current.present {
			enabled = def.DefaultEnabled
		}
		if patch.Enabled != nil {
			enabled = *patch.Enabled
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			return Internalf(err, "encode plugin settings")
		}
		var at time.Time
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO plugins (key, enabled, settings, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (key) DO UPDATE SET enabled = EXCLUDED.enabled, settings = EXCLUDED.settings, updated_at = now()
			RETURNING updated_at`, key, enabled, encoded).Scan(&at); err != nil {
			return Internalf(err, "write plugin")
		}
		out = assemble(def, pluginRow{enabled: enabled, settings: settings, updatedAt: &at, present: true})

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

// coercePluginValue turns what a form or a client sent into the field's own
// type, or refuses it with the field's name.
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
		return nil, Validationf("%s must be true or false", f.Label)
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
				return nil, Validationf("%s must be a number", f.Label)
			}
			return x, nil
		case string:
			if strings.TrimSpace(n) == "" {
				return nil, nil
			}
			var x float64
			if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &x); err != nil {
				return nil, Validationf("%s must be a number", f.Label)
			}
			return x, nil
		case nil:
			return nil, nil
		}
		return nil, Validationf("%s must be a number", f.Label)
	case "select":
		s, ok := v.(string)
		if v == nil || (ok && s == "") {
			return nil, nil
		}
		if !ok {
			return nil, Validationf("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
		}
		for _, o := range f.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, Validationf("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
	case "url":
		s, ok := v.(string)
		if v == nil || (ok && strings.TrimSpace(s) == "") {
			return nil, nil
		}
		if !ok || !(strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
			return nil, Validationf("%s must be an absolute URL", f.Label)
		}
		return strings.TrimSpace(s), nil
	default:
		if v == nil {
			return nil, nil
		}
		s, ok := v.(string)
		if !ok {
			return nil, Validationf("%s must be text", f.Label)
		}
		return strings.TrimSpace(s), nil
	}
}

// Enabled is whether a plugin is switched on — what a module asks before it
// does anything. An unknown key is off.
func (p *Plugins) Enabled(ctx context.Context, key string) (bool, error) {
	def, ok := p.def(key)
	if !ok {
		return false, nil
	}
	r, err := p.row(ctx, p.app.db, key)
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
