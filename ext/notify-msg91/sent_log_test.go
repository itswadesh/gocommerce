package msg91

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A text MSG91 took is written to the log, with the id MSG91 filed it under,
// as an email Resend took is: without the line a text that went out and one
// that was never tried look the same to whoever reads the log. The code it
// carries is never written down.
func TestASentTextIsLogged(t *testing.T) {
	var logged bytes.Buffer
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.NewWithConfig(t, gocommerce.Config{
		AllowPrivateURLs: true,
		Logger:           slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}, New(Config{BaseURL: server.URL, AuthKey: "k", Templates: map[string]string{EventLoginCode: "tpl-signin"}}))
	if err := app.Notify(context.Background(), codeNote(EventLoginCode, "0420")); err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(logged.String(), "\n") {
		if strings.Contains(l, `msg="text sent"`) {
			line = l
		}
	}
	for _, want := range []string{"event=identity.login_code", "template=tpl-signin", "to=919876543210", "id=queued"} {
		if !strings.Contains(line, want) {
			t.Fatalf("sent line %q lacks %s", line, want)
		}
	}
	if strings.Contains(logged.String(), "0420") {
		t.Fatal("the sign-in code was written to the log")
	}
}
