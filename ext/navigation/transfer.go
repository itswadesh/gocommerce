package navigation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Menus as a spreadsheet.
//
// A menu is a tree, and a tree does not fit a spreadsheet as a parent column:
// an operator editing in Excel cannot be asked to keep ids consistent, and
// ids mean nothing in the store the file came from. So the trail of titles
// rides in one cell — "Shop / Shirts" — which is legible on its own and is
// the same trick the category file uses.
//
// The file's order is the menu's order. There is no position column, because
// a menu is a list and a list in a spreadsheet is its rows: dragging row six
// above row three is how somebody reorders a menu in a spreadsheet, and a
// position column that disagreed with the row order would only be a second
// source of truth to get wrong.
//
// A menu the file names is written whole — every item under that handle is
// replaced by the file's rows — because whole is how a menu is edited here
// and how the API already saves one. A menu the file does not name is not
// touched, so a header file cannot take the footer with it.

// menuCSVHeader is the file's layout. `menu` is the handle and `menu_title`
// is read only when the file is creating a menu the store does not have.
var menuCSVHeader = []string{"menu", "menu_title", "path", "kind", "target"}

func (m *Module) mountTransferRoutes(app *gocommerce.App) {
	app.HandleAdminFunc("GET /api/admin/x/navigation/export", m.handleExport, gocommerce.RightDataExport)
	app.HandleAdminFunc("POST /api/admin/x/navigation/import", m.handleImport, gocommerce.RightDataImport)
}

func (m *Module) handleExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="menus-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	if err := m.Export(r.Context(), w); err != nil {
		m.app.Log().Error("menu export failed midway", "error", err)
	}
}

func (m *Module) handleImport(w http.ResponseWriter, r *http.Request) {
	dry := false
	switch r.URL.Query().Get("dry_run") {
	case "1", "true", "yes", "on":
		dry = true
	}
	result, err := m.Import(r.Context(), http.MaxBytesReader(w, r.Body, 8<<20), dry)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, result)
}

// Export streams every menu, each item under its parent and in its order.
func (m *Module) Export(ctx context.Context, out io.Writer) error {
	w, err := gocommerce.NewCSVWriter(out, menuCSVHeader)
	if err != nil {
		return err
	}
	menus, err := m.db.QueryContext(ctx, `SELECT id, handle, title FROM navigation_menus ORDER BY handle`)
	if err != nil {
		return gocommerce.Internalf(err, "read the menus")
	}
	var list []menuHead
	for menus.Next() {
		var mr menuHead
		if err := menus.Scan(&mr.id, &mr.handle, &mr.title); err != nil {
			menus.Close()
			return gocommerce.Internalf(err, "scan the menus")
		}
		list = append(list, mr)
	}
	err = menus.Err()
	menus.Close()
	if err != nil {
		return gocommerce.Internalf(err, "read the menus")
	}

	for _, mr := range list {
		// The module's own reader, so the file and the screen cannot come to
		// disagree about what is in a menu. paths{} is enough: the resolved
		// URL is a computed convenience the file does not carry.
		items, _, err := m.tree(ctx, mr.id, paths{})
		if err != nil {
			return err
		}
		if err := writeMenuItems(w, mr, items, nil); err != nil {
			return err
		}
	}
	return w.Flush()
}

// menuHead is a menu's own columns, repeated on each of its rows so that a
// row is legible on its own and a sorted file still says what it means.
type menuHead struct {
	id            int64
	handle, title string
}

// writeMenuItems walks a menu depth first, carrying the trail of titles that
// becomes the path cell.
func writeMenuItems(w *gocommerce.CSVWriter, mr menuHead, items []*Item, trail []string) error {
	for _, it := range items {
		path := append(append([]string{}, trail...), it.Title)
		if err := w.Write([]string{
			mr.handle, mr.title, strings.Join(path, " / "), it.Kind, it.Target,
		}); err != nil {
			return err
		}
		if err := writeMenuItems(w, mr, it.Children, path); err != nil {
			return err
		}
	}
	return nil
}

// Import replaces every menu the file names, and leaves every menu it does
// not name alone.
//
// Created and Updated count menus rather than rows, because a menu is written
// whole: reporting "412 updated" for one menu rebuilt would say nothing an
// operator can act on. Skipped counts the rows that were refused.
func (m *Module) Import(ctx context.Context, in io.Reader, dryRun bool) (*gocommerce.ImportResult, error) {
	r, err := gocommerce.NewCSVReader(in)
	if err != nil {
		return nil, err
	}
	for _, col := range []string{"menu", "path"} {
		if !r.Has(col) {
			return nil, gocommerce.Validationf("the CSV is missing the required column %q", col)
		}
	}

	result := &gocommerce.ImportResult{DryRun: dryRun}
	// Menus in the order the file first names them, so the report and any
	// error read in the file's own order rather than a map's.
	order := []string{}
	byHandle := map[string]*menuBuild{}

	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, gocommerce.RowError{Line: r.Line(), Message: err.Error()})
			result.Skipped++
			continue
		}
		line := row.Line()
		fail := func(format string, args ...any) {
			result.Errors = append(result.Errors, gocommerce.RowError{Line: line, Message: fmt.Sprintf(format, args...)})
			result.Skipped++
		}

		handle := strings.ToLower(strings.TrimSpace(row.Get("menu")))
		if handle == "" {
			fail("menu is empty; every row has to say which menu it belongs to")
			continue
		}
		// The Menus screen's own rule. Letting a file in by a side door would
		// build a menu that screen can never rename, and a handle that reads
		// wrong in a storefront URL.
		if !handleRE.MatchString(handle) {
			fail("menu %q is not a handle: lowercase letters, digits and single dashes", handle)
			continue
		}
		build, ok := byHandle[handle]
		if !ok {
			build = &menuBuild{handle: handle, byPath: map[string]*Item{}}
			byHandle[handle] = build
			order = append(order, handle)
		}
		if build.title == "" {
			build.title = strings.TrimSpace(row.Get("menu_title"))
		}

		parts := splitMenuPath(row.Get("path"))
		if len(parts) == 0 {
			fail("path is empty; a row has to say where the item sits, as in %q", "Shop / Shirts")
			continue
		}
		// Checked here as well as in validateItems below, so that one path
		// too deep is one row with a line number on it. Reaching validateItems
		// with it would fail the whole file and leave the operator hunting
		// through a thousand rows for the one that did it.
		if len(parts) > maxMenuDepth {
			fail("that path is %d deep and a menu goes at most %d levels", len(parts), maxMenuDepth)
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(row.Get("kind")))
		if kind == "" {
			kind = KindURL
		}
		if !kinds[kind] {
			fail("kind %q is not url, home, product, collection, category or page", kind)
			continue
		}
		target := strings.TrimSpace(row.Get("target"))
		if kind != KindHome && target == "" {
			fail("%q needs a link: a URL, or the handle of what it points at", parts[len(parts)-1])
			continue
		}

		item := &Item{Title: parts[len(parts)-1], Kind: kind, Target: target}
		key := strings.ToLower(strings.Join(parts, " / "))
		if _, clash := build.byPath[key]; clash {
			fail("%q appears twice in the menu %q", strings.Join(parts, " / "), handle)
			continue
		}
		if len(parts) == 1 {
			build.items = append(build.items, item)
		} else {
			parentKey := strings.ToLower(strings.Join(parts[:len(parts)-1], " / "))
			parent, ok := build.byPath[parentKey]
			if !ok {
				// An item cannot be hung on something the file has not put
				// there yet. Creating the parent would mean inventing a link
				// for it, and an invented link is a broken menu item.
				fail("%q sits under %q, which no row above it creates",
					item.Title, strings.Join(parts[:len(parts)-1], " / "))
				continue
			}
			parent.Children = append(parent.Children, item)
		}
		build.byPath[key] = item
	}

	if len(order) == 0 {
		return nil, gocommerce.Validationf("that file names no menus")
	}
	for _, handle := range order {
		// The screen's own rule, applied to the file: four levels, a title on
		// every item, a link on everything but Home.
		if err := validateItems(byHandle[handle].items, 0); err != nil {
			return nil, err
		}
	}

	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		for _, handle := range order {
			build := byHandle[handle]
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM navigation_menus WHERE handle = $1`, handle).Scan(&id)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				title := build.title
				if title == "" {
					// By rune, not by byte: handle[:1] cuts a multi-byte
					// first letter in half and stores invalid UTF-8.
					letters := []rune(handle)
					letters[0] = unicode.ToUpper(letters[0])
					title = string(letters)
				}
				if err := tx.QueryRowContext(ctx,
					`INSERT INTO navigation_menus (handle, title) VALUES ($1, $2) RETURNING id`,
					handle, title).Scan(&id); err != nil {
					return err
				}
				result.Created++
			case err != nil:
				return err
			default:
				if _, err := tx.ExecContext(ctx,
					`DELETE FROM navigation_items WHERE menu_id = $1`, id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx,
					`UPDATE navigation_menus SET updated_at = now() WHERE id = $1`, id); err != nil {
					return err
				}
				result.Updated++
			}
			if err := insertItems(ctx, tx, id, nil, build.items); err != nil {
				return err
			}
		}
		if dryRun {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return nil, err
	}
	return result, nil
}

// errDryRun rolls a rehearsal back once it has proved the file applies.
var errDryRun = errors.New("dry run")

// menuBuild is one menu's tree, assembled as the file is read.
type menuBuild struct {
	handle, title string
	items         []*Item
	// byPath finds the parent of the row being read. Lower-cased, because a
	// file somebody retyped should not fail on a capital letter.
	byPath map[string]*Item
}

// splitMenuPath reads a trail in either separator, as the category file does:
// "Shop / Shirts" or "Shop > Shirts".
func splitMenuPath(path string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '>' }) {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}
