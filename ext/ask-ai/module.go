// Package askai is "Ask AI" in the admin panel (D233): a person asks in plain
// words how to set up their store, or asks for its details to be changed.
//
// A guide answer is words and links to real screens. A change is a proposal in
// the gateway's approval queue (D105), drawn as each field's value now and its
// new value, and nothing is written until the person presses Apply. The model
// only ever suggests: its words never reach a write except as arguments that go
// through the allowlist in action.go and the store profile's own validation,
// with a preview recomputed at Apply.
//
//	gw := aigateway.New(aigateway.Config{})
//	app, err := gocommerce.New(cfg, gw, askai.New(askai.Config{Gateway: gw}))
package askai

import (
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// RightUse is asking. Applying a change needs store.write besides, checked when
// it is applied.
const RightUse gocommerce.Right = "ask-ai.use"

// Config configures the module.
type Config struct {
	// Gateway is the store's AI gateway. Required: it is the only way this
	// module calls a model.
	Gateway *aigateway.Module
}

// Module is Ask AI.
type Module struct {
	cfg  Config
	app  *gocommerce.App
	once sync.Once

	limitMu sync.Mutex
	limits  map[limitKey]*limitWindow
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, limits: map[limitKey]*limitWindow{}} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "ask-ai" }

// Migrations implements gocommerce.Module. Ask AI keeps nothing of its own: the
// conversation lives in the browser and a proposal in the gateway's queue.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	app.RegisterRight(gocommerce.RightSpec{
		Right: RightUse,
		Label: "Ask AI",
		Scope: "Asking Ask AI about setting up the store, which spends the store's AI credit. Changing anything also needs the right that change needs",
		// Every default role that can sign in and work: what it answers from is
		// already open to any operator, GET /api/admin/setup and
		// GET /api/admin/store. A vendor is a stranger to the store's set-up.
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	if m.cfg.Gateway == nil {
		app.Log().Warn("ask-ai is installed without the AI gateway, so it can answer nothing")
	} else {
		var err error
		m.once.Do(func() {
			if err = m.cfg.Gateway.RegisterTask(m.task()); err == nil {
				err = m.cfg.Gateway.RegisterAction(m.action())
			}
		})
		if err != nil {
			return err
		}
	}
	app.HandleAdminFunc("GET /api/admin/x/ask-ai/status", m.handleStatus, RightUse)
	app.HandleAdminFunc("POST /api/admin/x/ask-ai/turn", m.handleTurn, RightUse)
	app.HandleAdminFunc("POST /api/admin/x/ask-ai/proposals/{id}/apply", m.handleApply, gocommerce.RightStoreWrite)
	app.HandleAdminFunc("POST /api/admin/x/ask-ai/proposals/{id}/dismiss", m.handleDismiss, gocommerce.RightStoreWrite)
	return nil
}

// A person's turns are limited per process: each one holds a model call open.
//
// ponytail: in memory, so a second engine doubles it and a restart resets it.
// The credit and the monthly cap are what bound the money; this only stops a
// stuck key or a script from spending them in a minute. Move it to Postgres
// beside api_key_rates if the engine runs several replicas.
const (
	turnsPerWindow = 30
	turnWindow     = 10 * time.Minute
)

type limitKey struct {
	store  string
	person int64
}

type limitWindow struct {
	start time.Time
	used  int
}

// allowTurn counts one turn, and says how long to wait when there is none left.
func (m *Module) allowTurn(key limitKey, now time.Time) (time.Duration, bool) {
	m.limitMu.Lock()
	defer m.limitMu.Unlock()
	w := m.limits[key]
	if w == nil || now.Sub(w.start) >= turnWindow {
		w = &limitWindow{start: now}
		m.limits[key] = w
	}
	if w.used >= turnsPerWindow {
		return turnWindow - now.Sub(w.start), false
	}
	w.used++
	return 0, true
}
