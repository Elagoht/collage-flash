package flash_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	flash "github.com/Elagoht/collage-flash"
	"github.com/Elagoht/collage/pkg/collage"
)

var key = bytes.Repeat([]byte("k"), 32)

func site(t *testing.T) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<main>{{range flashes}}<p class="{{.Kind}}">{{.Text}}</p>{{end}}settings</main>`)},
		}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Plugins: []collage.Plugin{flash.New(flash.Options{Key: key})},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Static, so it is cached: a message must never end up in the cached copy.
	if err := app.RegisterPage(collage.NewPage("settings").WithContent(collage.NewFragment("settings", "p.html").Build()).WithPath("en", "/settings").Static().Build()); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*collage.Action{
		collage.NewAction("save").WithPath("en", "/save").WithMethods(http.MethodGet).WithHandler(
			func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
				flash.Add(rc, flash.Success, "Saved <b>everything</b>")
				return collage.SeeOther("/settings"), nil
			}).Build(),
		collage.NewAction("bounce").WithPath("en", "/bounce").WithMethods(http.MethodGet).WithHandler(
			func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
				flash.Add(rc, flash.Info, "Bounced")
				return collage.SeeOther("/settings"), nil
			}).Build(),
	} {
		if err := app.RegisterAction(a); err != nil {
			t.Fatal(err)
		}
	}
	return app.Handler()
}

func get(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func flashCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "collage_flash" {
			return c
		}
	}
	return nil
}

// Set by the action, shown once by the page it redirects to, then gone — and never
// in the cached page someone else is served.
func TestFlashRoundTrip(t *testing.T) {
	h := site(t)
	get(h, "/settings") // cache the page without a message

	saved := get(h, "/save")
	c := flashCookie(saved)
	if saved.Code != http.StatusSeeOther || c == nil || c.Value == "" || !c.HttpOnly {
		t.Fatalf("action: %d, cookie %+v", saved.Code, c)
	}

	shown := get(h, "/settings", c)
	if !strings.Contains(shown.Body.String(), `<p class="success">Saved &lt;b&gt;everything&lt;/b&gt;</p>`) {
		t.Errorf("the page does not show the message:\n%s", shown.Body.String())
	}
	if cleared := flashCookie(shown); cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("the message is not cleared once shown: %+v", cleared)
	}
	if cc := shown.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("a page with a message is cacheable: %q", cc)
	}

	other := get(h, "/settings")
	if strings.Contains(other.Body.String(), "Saved") {
		t.Errorf("another reader was shown the message:\n%s", other.Body.String())
	}
}

// A cookie a reader wrote is not a message the site sent.
func TestForgedCookie(t *testing.T) {
	h := site(t)
	saved := get(h, "/save")
	c := flashCookie(saved)
	forged := *c
	forged.Value = strings.Replace(c.Value, ".", "x.", 1)
	if body := get(h, "/settings", &forged).Body.String(); strings.Contains(body, "Saved") {
		t.Errorf("a forged cookie was shown:\n%s", body)
	}
}

// A message not yet shown rides along with a new one.
func TestUnshownMessagesAreKept(t *testing.T) {
	h := site(t)
	first := flashCookie(get(h, "/save"))
	second := flashCookie(get(h, "/bounce", first))
	body := get(h, "/settings", second).Body.String()
	if !strings.Contains(body, "Saved") || !strings.Contains(body, "Bounced") {
		t.Errorf("both messages should show:\n%s", body)
	}
}

// Outside a request the plugin wraps, adding and taking do nothing.
func TestOutsideARequest(t *testing.T) {
	flash.Add(nil, flash.Info, "nothing")
	flash.AddTo(context.Background(), flash.Info, "nothing")
	if got := flash.Take(nil); got != nil {
		t.Errorf("Take = %v", got)
	}
}
