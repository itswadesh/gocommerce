package retailassistant

import (
	"context"
	"errors"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// loginCodes carries core's sign-in code on the install's WhatsApp number. It is
// only a way to deliver a message. Core makes the code, limits the tries, checks
// it and issues the session (D193), so there is no second way into the panel and
// nothing here can mint one.
type loginCodes struct{ m *Module }

// Configured implements gocommerce.Configurable.
func (l loginCodes) Configured(context.Context) bool {
	return l.m.cfg.LoginTemplate != "" && l.m.channelReady()
}

// Serves implements gocommerce.ScopedNotifier, and takes every number alike.
// Serving only linked phones would look kinder, but core refuses a number no
// channel carries, so anybody could then ask which phones are linked to a store;
// D193 keeps sign-in from saying anything about who a number belongs to.
func (l loginCodes) Serves(_ context.Context, n gocommerce.Notification) bool {
	return n.Event == gocommerce.EventLoginCode && n.Channel == gocommerce.ChannelSMS
}

// Notify sends an approved authentication template: Meta requires the code twice,
// in the body and in the copy-code button. A failure says nothing Meta sent back,
// because this error reaches logs and its request held a live code.
func (l loginCodes) Notify(ctx context.Context, n gocommerce.Notification) error {
	code := n.Data["code"]
	if code == "" {
		return errors.New("a sign-in message came with no code")
	}
	lang := langEnglish
	if n.Language == langHindi {
		lang = langHindi
	}
	ctx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	err := l.m.post(ctx, map[string]any{
		"messaging_product": "whatsapp", "to": digits(n.To), "type": "template",
		"template": map[string]any{
			"name": l.m.cfg.LoginTemplate, "language": map[string]string{"code": lang},
			"components": []map[string]any{
				{"type": "body", "parameters": []map[string]string{{"type": "text", "text": code}}},
				{"type": "button", "sub_type": "url", "index": "0", "parameters": []map[string]string{{"type": "text", "text": code}}},
			},
		},
	})
	if err != nil {
		return errors.New("WhatsApp did not take the sign-in code")
	}
	return nil
}
