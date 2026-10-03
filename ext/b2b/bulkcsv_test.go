package b2b

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// sendCSV posts a file to the quick-order route as a buyer.
func (f *fixture) sendCSV(t *testing.T, token, target, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	return rec
}

// A spreadsheet goes in as a pasted order does: its header in any case, a
// byte-order mark and all, the columns it does not need ignored, and every
// line that would not go in named by the row a spreadsheet shows it on.
func TestABulkOrderReadsASpreadsheet(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)
	gctest.CreateProduct(t, f.app, "B2B-SCARCE", 2000, 2)
	gctest.CreateProduct(t, f.app, "-DASH", 1000, 50)

	basket := f.basket(t)
	file := "\xef\xbb\xbfTitle,SKU,Quantity\r\n" +
		"Widget,B2B-WIDGET,3\r\n" +
		"Nobody sells this,NO-SUCH-SKU,1\r\n" +
		",,\r\n" +
		"Scarce,B2B-SCARCE,5\r\n" +
		"Widget again,B2B-WIDGET,two\r\n" +
		"A formula-looking SKU,'-DASH,4\r\n"
	rec := f.sendCSV(t, tok, "/x/b2b/cart/lines?cart_id="+basket, "text/csv; charset=utf-8", file)
	if rec.Code != http.StatusOK {
		t.Fatalf("csv = %d: %s", rec.Code, rec.Body)
	}
	var fill CartFill
	gctest.DecodeData(t, rec, &fill)
	if fill.Cart.Token != basket {
		t.Errorf("filled basket %s, want the one named in cart_id", fill.Cart.Token)
	}
	lines := f.lines(t, basket)
	if len(lines) != 2 || lines["B2B-WIDGET"].Quantity != 3 || lines["B2B-WIDGET"].UnitPrice.AmountMinor != 8000 ||
		lines["-DASH"].Quantity != 4 {
		t.Errorf("basket = %+v; want 3 widgets at the company's price and 4 of -DASH", lines)
	}
	want := map[int]string{3: RejectNotFound, 5: RejectInsufficientStock, 6: RejectInvalid}
	if len(fill.Rejected) != len(want) {
		t.Fatalf("rejected = %+v, want rows 3, 5 and 6", fill.Rejected)
	}
	for _, r := range fill.Rejected {
		if want[r.Row] != r.Reason {
			t.Errorf("row %d rejected as %q, want %q (%+v)", r.Row, r.Reason, want[r.Row], r)
		}
	}

	// By variant id, into a new basket, when no cart_id is named.
	byID := f.sendCSV(t, tok, "/x/b2b/cart/lines", "text/csv",
		"variant_id,quantity\n"+strconv.FormatInt(f.variant, 10)+",2\n")
	if byID.Code != http.StatusOK {
		t.Fatalf("by variant id = %d: %s", byID.Code, byID.Body)
	}
	gctest.DecodeData(t, byID, &fill)
	if fill.Cart.Token == basket || len(fill.Cart.Lines) != 1 || fill.Cart.Lines[0].Quantity != 2 {
		t.Errorf("new basket = %+v; want 2 widgets in a basket of its own", fill.Cart)
	}
}

// A file that cannot be read as an order is refused whole, saying why: there
// is no line in it to reject.
func TestABulkOrderFileWithoutItsColumnsIsRefused(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	for name, file := range map[string]string{
		"no quantity column": "sku,qty\nB2B-WIDGET,1\n",
		"no sku column":      "name,quantity\nWidget,1\n",
		"nothing under it":   "sku,quantity\n",
		"empty":              "",
		"a broken quote":     "sku,quantity\n\"B2B-WIDGET,1\n",
	} {
		rec := f.sendCSV(t, tok, "/x/b2b/cart/lines", "text/csv", file)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"message"`) {
			t.Errorf("%s = %d %s, want 400 with a message", name, rec.Code, rec.Body)
		}
	}
	if rec := f.sendCSV(t, tok, "/x/b2b/cart/lines", "text/csv", "sku,quantity\nB2B-WIDGET,1\n"); rec.Code != http.StatusOK {
		t.Errorf("the same file with its columns = %d %s", rec.Code, rec.Body)
	}

	var big strings.Builder
	big.WriteString("sku,quantity\n")
	for range maxBulkLines + 1 {
		big.WriteString("B2B-WIDGET,1\n")
	}
	if rec := f.sendCSV(t, tok, "/x/b2b/cart/lines", "text/csv", big.String()); rec.Code != http.StatusBadRequest {
		t.Errorf("%d lines = %d, want 400", maxBulkLines+1, rec.Code)
	}
}
