package faq

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop's questions and answers (D96): what a shopper asks before
// buying, answered the way a small shop might, for the merchant to rewrite.

// migrationSample marks the entries the sample shop added, by a column no
// request sets, so erasing them never takes the shop's own.
const migrationSample = `ALTER TABLE faq_entries ADD COLUMN sample boolean NOT NULL DEFAULT false;`

var sampleEntries = []struct{ section, question, answer string }{
	{"Orders", "How do I know my order went through?",
		"You get an email with your order number as soon as it is placed. If it has not arrived within a few minutes, check your spam folder."},
	{"Orders", "Can I change or cancel my order?",
		"Write to us as soon as you can. Until an order is packed we can change it or cancel it."},
	{"Delivery", "How long does delivery take?",
		"Most orders are packed within two working days. You get an email with tracking once yours is on its way."},
	{"Delivery", "Where do you deliver?",
		"Anywhere the checkout offers a delivery option for your address."},
	{"Returns", "Can I return something?",
		"Yes. Send it back unused within 30 days and we refund you once it reaches us."},
}

// AddSamples implements gocommerce.SampleSeeder. A question the shop already
// asks is skipped, each new one last in its section.
func (m *Module) AddSamples(ctx context.Context, _ *gocommerce.SampleShop) (gocommerce.SampleCounts, error) {
	added := 0
	for _, e := range sampleEntries {
		res, err := m.db.ExecContext(ctx, `
			INSERT INTO faq_entries (question, answer, section, position, sample)
			SELECT $1, $2, $3, coalesce(max(position), -1) + 1, true FROM faq_entries WHERE section = $3
			HAVING NOT EXISTS (SELECT 1 FROM faq_entries WHERE question = $1)`,
			e.question, e.answer, e.section)
		if err != nil {
			return gocommerce.SampleCounts{"faq": added}, gocommerce.Internalf(err, "add the sample questions")
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return gocommerce.SampleCounts{"faq": added}, nil
}

// CountSamples implements gocommerce.SampleSeeder.
func (m *Module) CountSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM faq_entries WHERE sample`).Scan(&n); err != nil {
		return nil, gocommerce.Internalf(err, "count the sample questions")
	}
	return gocommerce.SampleCounts{"faq": n}, nil
}

// EraseSamples implements gocommerce.SampleSeeder.
func (m *Module) EraseSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM faq_entries WHERE sample`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "erase the sample questions")
	}
	n, _ := res.RowsAffected()
	return gocommerce.SampleCounts{"faq": int(n)}, nil
}
