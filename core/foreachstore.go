package gocommerce

import (
	"context"
	"errors"
	"fmt"
)

// ForEachStore runs fn once per active store, bound to it, oldest store
// first. Background work goes through here because under `serve` a context
// with no store sees no rows: a sweep that forgot to name its store would do
// nothing, silently. One store's failure is logged and does not stop the
// others; the errors come back joined.
func (a *App) ForEachStore(ctx context.Context, fn func(ctx context.Context) error) error {
	stores, err := a.stores.Active(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range stores {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := fn(WithStore(ctx, s.ID)); err != nil {
			a.log.Error("store job failed", "store", s.ID, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", s.ID, err))
		}
	}
	return errors.Join(errs...)
}
