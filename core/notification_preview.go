package gocommerce

import (
	"context"
	"net/http"
)

// A message from the notification log, as it reads.
//
// The log keeps what a message was about — its event, its language, the data
// it was filled with — and not its words, which are the store's wording at
// the time it went out. So the preview fills today's wording with the data
// the message carried, in the language it was sent in, and says so when the
// wording has been edited since: that message may have read differently.

// NotificationPreview is one logged message, rendered.
type NotificationPreview struct {
	Channel  string `json:"channel"`
	Language string `json:"language"`
	// Subject is an email's subject line; empty for a text.
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
	// Source is whose wording it is: the language's own, the main
	// language's, or the engine's default (NotifyTemplate.Source).
	Source string `json:"source"`
	// EditedSince is true when the wording was changed after the message
	// went out, so the message itself may have read differently.
	EditedSince bool `json:"edited_since"`
}

// Preview renders the logged message id with the wording as it is now.
func (n *Notifications) Preview(ctx context.Context, id int64) (*NotificationPreview, error) {
	rec, err := n.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n.app.maskNotification(ctx, rec)
	tpl, ok, err := n.app.notifyTemplates.GetIn(ctx, rec.Channel, rec.Event, rec.Language)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, NotFoundf("no wording is kept for %q messages, so there is nothing to show", rec.Event)
	}
	out := &NotificationPreview{Channel: rec.Channel, Language: tpl.Language, Source: tpl.Source,
		EditedSince: tpl.UpdatedAt != nil && tpl.UpdatedAt.After(rec.CreatedAt)}
	if rec.Channel == ChannelEmail {
		if out.Subject, err = RenderNotifyText(tpl.Subject, rec.Data); err != nil {
			return nil, Validationf("the subject's wording does not render: %v", err)
		}
	}
	if out.Body, err = RenderNotifyText(tpl.Body, rec.Data); err != nil {
		return nil, Validationf("the wording does not render: %v", err)
	}
	return out, nil
}

func (a *App) handlePreviewNotification(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.notifications.Preview(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}
