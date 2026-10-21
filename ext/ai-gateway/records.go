package aigateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Execution is one run's record (ADR 0005, the spec's AIExecution): what
// asked, what answered, what it cost and what was allowed to leave.
type Execution struct {
	ID           int64       `json:"id"`
	Task         string      `json:"task"`
	TaskVersion  int         `json:"task_version"`
	JobID        *int64      `json:"job_id,omitempty"`
	TraceID      string      `json:"trace_id"`
	Steps        []Step      `json:"steps"`
	Provider     string      `json:"provider"`
	Model        string      `json:"model"`
	ModelVersion string      `json:"model_version"`
	Usage        Usage       `json:"usage"`
	LatencyMS    int64       `json:"latency_ms"`
	Outcome      string      `json:"outcome"`
	ReviewStatus string      `json:"review_status"`
	Confidence   *int        `json:"confidence,omitempty"`
	SourceRefs   []SourceRef `json:"source_refs"`
	Policy       Policy      `json:"policy"`
	// CostMicroMinor is the run's cost in millionths of a minor unit of
	// Currency: every call it made, at the prices set when it ran.
	CostMicroMinor int64     `json:"cost_micro_minor"`
	Currency       string    `json:"currency"`
	CreatedAt      time.Time `json:"created_at"`
}

func (m *Module) writeExecution(ctx context.Context, tx *sql.Tx, e *Execution) (int64, error) {
	steps, err := json.Marshal(e.Steps)
	if err != nil {
		return 0, err
	}
	refs, err := json.Marshal(e.SourceRefs)
	if err != nil {
		return 0, err
	}
	policy, err := json.Marshal(e.Policy)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ai_executions (task, task_version, job_id, trace_id, steps, provider, model, model_version,
		                           input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, latency_ms,
		                           outcome, review_status, confidence, source_refs, policy, cost_micro_minor, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id`,
		e.Task, e.TaskVersion, e.JobID, e.TraceID, steps, e.Provider, e.Model, e.ModelVersion,
		e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CacheReadTokens, e.Usage.CacheWriteTokens, e.LatencyMS,
		e.Outcome, e.ReviewStatus, e.Confidence, refs, policy, e.CostMicroMinor, e.Currency).Scan(&id)
	return id, err
}

const executionColumns = `id, task, task_version, job_id, trace_id, steps, provider, model, model_version,
	input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, latency_ms, outcome, review_status,
	confidence, source_refs, policy, cost_micro_minor, currency, created_at`

func scanExecution(row interface{ Scan(...any) error }) (*Execution, error) {
	var e Execution
	var steps, refs, policy []byte
	var confidence sql.NullInt64
	if err := row.Scan(&e.ID, &e.Task, &e.TaskVersion, &e.JobID, &e.TraceID, &steps, &e.Provider, &e.Model,
		&e.ModelVersion, &e.Usage.InputTokens, &e.Usage.OutputTokens, &e.Usage.CacheReadTokens,
		&e.Usage.CacheWriteTokens, &e.LatencyMS, &e.Outcome, &e.ReviewStatus, &confidence, &refs, &policy,
		&e.CostMicroMinor, &e.Currency, &e.CreatedAt); err != nil {
		return nil, err
	}
	if confidence.Valid {
		c := int(confidence.Int64)
		e.Confidence = &c
	}
	for _, part := range []struct {
		raw []byte
		dst any
	}{{steps, &e.Steps}, {refs, &e.SourceRefs}, {policy, &e.Policy}} {
		if err := json.Unmarshal(part.raw, part.dst); err != nil {
			return nil, err
		}
	}
	return &e, nil
}

// Execution reads one run of ctx's store.
func (m *Module) Execution(ctx context.Context, id int64) (*Execution, error) {
	e, err := scanExecution(m.db.QueryRowContext(ctx,
		`SELECT `+executionColumns+` FROM ai_executions WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no AI run %d", id)
	}
	return e, err
}

// ExecutionQuery filters the runs.
type ExecutionQuery struct {
	Task, Outcome string
	JobID         int64
	Limit, Offset int
}

// Executions is a page of ctx's store's runs, newest first.
func (m *Module) Executions(ctx context.Context, q ExecutionQuery) ([]*Execution, int, error) {
	where, args := []string{"true"}, []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if q.Task != "" {
		add("task = $%d", q.Task)
	}
	if q.Outcome != "" {
		add("outcome = $%d", q.Outcome)
	}
	if q.JobID > 0 {
		add("job_id = $%d", q.JobID)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_executions WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if q.Limit <= 0 {
		q.Limit = gocommerce.DefaultLimit
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+executionColumns+` FROM ai_executions WHERE `+clause+
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Execution{}
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
