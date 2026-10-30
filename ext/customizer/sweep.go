package customizer

import (
	"context"
	"database/sql"
	"fmt"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const sweepBatch = 200

var retention = fmt.Sprintf("%d days", RetentionDays)

// Sweep deletes the context's store's designs and uploads that nobody ordered
// and nobody has touched for RetentionDays, and says how many of each went.
//
// A design is kept while a basket holds it or an order line names it, and an
// upload while any kept design names it. Order lines are where an ordered
// design lives, not the checked-out basket, which is kept for ever. A line
// an edit takes off an order lets its design go at the first sweep after
// RetentionDays from when the design was made or last vouched for, which
// for an older order is the next one. Orders are not otherwise deleted,
// except a sample shop's and a deleted store's, whose rows all go with it.
// Each upload's row is deleted before its file, in a transaction that skips
// rows a design being saved holds, and the file goes after the commit: a
// crash between the two leaves a file with no row, which is logged, never a
// design whose photo is gone.
//
// Exported for the reason layouts' PublishDue is: a test, or an operator in a
// hurry, need not wait out a tick.
func (m *Module) Sweep(ctx context.Context) (designs, uploads int, err error) {
	for {
		// The age is asked again of the row being deleted, not only of the
		// candidate: a basket vouching for the design meanwhile moved
		// vouched_at, and the row is re-read once its lock is free.
		res, err := m.db.ExecContext(ctx, `
			DELETE FROM customizer_designs d
			WHERE d.id IN (SELECT c.id FROM customizer_designs c
			               WHERE greatest(c.created_at, c.vouched_at) < now() - $1::interval
			                 AND NOT EXISTS (SELECT 1 FROM cart_line_items l JOIN carts k ON k.id = l.cart_id
			                                 WHERE l.design_id = c.id AND k.status <> 'converted')
			                 AND NOT EXISTS (SELECT 1 FROM order_lines o
			                                 WHERE o.metadata ? 'design' AND o.metadata->'design'->>'id' = c.id)
			               ORDER BY c.created_at LIMIT $2)
			  AND greatest(d.created_at, d.vouched_at) < now() - $1::interval`,
			retention, sweepBatch)
		if err != nil {
			return designs, uploads, gocommerce.Internalf(err, "sweep the designs")
		}
		n, _ := res.RowsAffected()
		designs += int(n)
		if n < sweepBatch {
			break
		}
	}

	store := m.app.MediaStore()
	for {
		var keys []string
		err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, `
				SELECT id, storage_key FROM customizer_uploads u
				WHERE u.created_at < now() - $1::interval
				  AND NOT EXISTS (SELECT 1 FROM customizer_design_uploads du WHERE du.upload_id = u.id)
				ORDER BY u.created_at LIMIT $2
				FOR UPDATE SKIP LOCKED`, retention, sweepBatch)
			if err != nil {
				return err
			}
			var ids []string
			for rows.Next() {
				var id, key string
				if err := rows.Scan(&id, &key); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
				keys = append(keys, key)
			}
			rows.Close()
			if err := rows.Err(); err != nil || len(ids) == 0 {
				return err
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM customizer_uploads WHERE id = ANY($1::text[])`, texts(ids))
			return err
		})
		if err != nil {
			return designs, uploads, gocommerce.Internalf(err, "sweep the uploads")
		}
		uploads += len(keys)
		for _, key := range keys {
			if store == nil {
				break
			}
			if err := store.Delete(ctx, key); err != nil {
				m.app.Log().Error("customizer: an expired upload's file was not deleted", "key", key, "error", err)
			}
		}
		if len(keys) < sweepBatch {
			return designs, uploads, nil
		}
	}
}
