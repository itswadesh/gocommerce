package aigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Task is a named piece of model work, written in Go (D107): its types, its
// checks and its ladder. The gateway runs it; nothing outside the gateway
// calls a model.
type Task struct {
	// Name is "area.verb", e.g. "product.rewrite".
	Name string
	// System is the task's instructions, the same for every store. It holds
	// no store data, ever: it is sent before anything the policy checks and
	// cached by the provider across stores. Store content goes in a
	// message's Data.
	System string
	// Version is bumped whenever System, Schema or Build change, so a cached
	// answer from the old version is never served and a record says which
	// version ran.
	Version int
	// Schema is the JSON Schema the answer must satisfy: an object, closed
	// (additionalProperties false) with every property required at every
	// level, which is what OpenAI's strict mode accepts and what Anthropic
	// takes as a tool's input. A property that may be absent is typed with
	// "null" beside its type instead. Only type, properties, required,
	// additionalProperties, items and enum constrain, with description and
	// title as notes: a length or range bound is checked in Check.
	Schema json.RawMessage
	// MaxInputTokens refuses a request built larger than this, unsent. With
	// MaxOutputTokens it is also the ceiling reserved against the cap.
	MaxInputTokens  int
	MaxOutputTokens int
	// Ladder is the models to try, in order: TierSmall, then TierStrong.
	Ladder []Tier
	// EscalateBelow moves an answer whose confidence is under it up the
	// ladder, as an answer that failed its checks is. 0 never escalates on
	// confidence alone.
	EscalateBelow int
	// CacheFor reuses an answer to the same input for this long, in the
	// same store only (D104). 0 never reuses one.
	CacheFor time.Duration
	// Deterministic is tried before any model: rules, lookups, a core
	// service call. ok=false passes to the model; an error ends the run.
	Deterministic func(ctx context.Context, input json.RawMessage) (out json.RawMessage, ok bool, err error)
	// Build turns the input into a request under the store's policy: the
	// Messages, with store content in Data tagged by class, and any
	// Documents. The gateway fills the model, instructions, schema and token
	// limit, and leaves out whatever the policy does not allow: a class that
	// is off, and contact details caught by their shape in other classes and
	// in a message's own Text. Data the task cannot do without is marked
	// Required, and a run whose required class is kept in stops.
	Build func(ctx context.Context, input json.RawMessage, p Policy) (Request, []SourceRef, error)
	// Check reads the answer once it has passed the schema, in the task's
	// own Go types, and says how confident it is, 0 to 100. An error sends
	// the answer up the ladder with the error as the reason. Nil accepts any
	// answer the schema accepts, with no confidence recorded.
	Check func(ctx context.Context, output json.RawMessage) (confidence int, err error)

	schema *schemaNode
}

// SourceRef names a row or document a run read, kept on its record.
type SourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

var taskNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*(\.[a-z0-9]+(-[a-z0-9]+)*)+$`)

// RegisterTask makes a task available to Run and Enqueue. It is refused,
// rather than half-kept, when the task could not run as written: a schema the
// gateway would not enforce in full is a schema a task author would wrongly
// rely on.
func (m *Module) RegisterTask(t Task) error {
	if !taskNameRE.MatchString(t.Name) {
		return fmt.Errorf("ai task %q: name it area.verb in lowercase, e.g. product.rewrite", t.Name)
	}
	if t.Version < 1 {
		return fmt.Errorf("ai task %s: version starts at 1", t.Name)
	}
	if len(t.Ladder) == 0 && t.Deterministic == nil {
		return fmt.Errorf("ai task %s: no ladder and no deterministic step, so nothing can answer it", t.Name)
	}
	for _, tier := range t.Ladder {
		if tier != TierSmall && tier != TierStrong {
			return fmt.Errorf("ai task %s: unknown tier %q", t.Name, tier)
		}
	}
	if len(t.Ladder) > 0 {
		if t.Build == nil {
			return fmt.Errorf("ai task %s: a task that calls a model needs Build", t.Name)
		}
		if t.MaxInputTokens <= 0 || t.MaxOutputTokens <= 0 {
			return fmt.Errorf("ai task %s: MaxInputTokens and MaxOutputTokens set the ceiling the cap is held to, and both are needed", t.Name)
		}
		if strings.TrimSpace(t.System) == "" {
			return fmt.Errorf("ai task %s: no instructions", t.Name)
		}
	}
	if t.EscalateBelow < 0 || t.EscalateBelow > 100 {
		return fmt.Errorf("ai task %s: EscalateBelow is a confidence, 0 to 100", t.Name)
	}
	schema, err := parseSchema(t.Schema)
	if err != nil {
		return fmt.Errorf("ai task %s: schema: %w", t.Name, err)
	}
	if schema.types[0] != "object" || len(schema.types) != 1 {
		return fmt.Errorf("ai task %s: schema: the answer must be an object", t.Name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[t.Name]; ok {
		return fmt.Errorf("ai task %s is already registered", t.Name)
	}
	task := t
	task.schema = schema
	m.tasks[t.Name] = &task
	return nil
}

func (m *Module) task(name string) *Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tasks[name]
}
