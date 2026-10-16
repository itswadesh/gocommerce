package showcases

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.NotFoundf("no such showcase")
	}
	return id, nil
}

// handleList is every showcase, or those with one status, with their totals.
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	var list []*Showcase
	var err error
	switch status {
	case "":
		list, err = m.list(r.Context(), "true")
	case StatusDraft, StatusPublished, StatusArchived:
		list, err = m.list(r.Context(), "status = $1", status)
	default:
		err = gocommerce.Validationf("status is draft, published or archived")
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, list)
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err := m.create(r.Context(), in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, s)
}

// handleCheck says what a pasted link would play, before anything is saved.
func (m *Module) handleCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	v, err := parseVideo(in.URL)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := m.check(r.Context(), v)
	out.URL = strings.TrimSpace(in.URL)
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err := m.get(r.Context(), "id = $1", id)
	if err == nil {
		err = m.forEditor(r.Context(), s)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in Input
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err := m.update(r.Context(), id, in)
	if err == nil {
		err = m.forEditor(r.Context(), s)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM showcases WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "delete showcase"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such showcase"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMakeCollection turns the products picked for a showcase into a
// collection of their own, in the same order, and shows that collection
// instead — so the set can be linked from a menu or reused by another
// showcase. It goes through the collections service, never the table.
func (m *Module) handleMakeCollection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	s, err := m.get(ctx, "id = $1", id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Picks deleted since are left out: the list keeps them, by design, and
	// a collection cannot.
	ids, err := m.present(ctx, s.ProductIDs)
	title := strings.TrimSpace(in.Title)
	switch {
	case err != nil:
	case title == "":
		err = gocommerce.Validationf("name the collection")
	case len(ids) == 0:
		err = gocommerce.Validationf("pick the showcase's products first; they become the collection")
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, err := m.app.Collections().Create(ctx, gocommerce.CollectionInput{Title: title})
	if err == nil {
		if err = m.app.Collections().SetCollectionProducts(ctx, c.ID, ids); err != nil {
			// Two service calls, so not one transaction: take the empty
			// collection back out rather than leave one per failed try.
			_ = m.app.Collections().Delete(context.WithoutCancel(ctx), c.ID)
		}
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err = m.update(ctx, id, Input{CollectionID: gocommerce.NullableID{Present: true, Value: &c.ID}})
	if err == nil {
		err = m.forEditor(ctx, s)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, s)
}
