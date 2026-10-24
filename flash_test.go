package flash_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	flash "github.com/Elagoht/collage-flash"
	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

var key = bytes.Repeat([]byte("k"), 32)

// site is the test site: a cached static page showing the messages, and two
// actions that add one and redirect to it. edits change its configuration.
func site(t *testing.T, edits ...func(*collage.Config)) http.Handler {
	t.Helper()
	opts := []collagetest.SiteOption{
		collagetest.WithPlugins(flash.New(flash.Options{Key: key})),
		collagetest.WithTemplates(map[string]string{
			"p.html": `<main>{{range flashes}}<p class="{{.Kind}}">{{.Text}}</p>{{end}}settings</main>`,
		}),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
		}),
		// Static, so it is cached: a message must never end up in the cached copy.
		collagetest.WithRegister(
			collage.NewPage("settings").WithContent(collage.NewFragment("settings", "p.html").Build()).WithPath("en", "/settings").Static().Build(),
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
		),
	}
	for _, edit := range edits {
		opts = append(opts, collagetest.WithConfig(edit))
	}
	return collagetest.NewSite(t, opts...).App.Handler()
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

// The cookie is Secure when the request came over HTTPS, as collage.IsHTTPS
// decides: X-Forwarded-Proto counts only from Server.TrustedProxies.
func TestSecureFollowsTrustedProxies(t *testing.T) {
	h := site(t, func(c *collage.Config) { c.Server.TrustedProxies = []string{"10.0.0.1"} })
	send := func(remote string) *http.Cookie {
		r := httptest.NewRequest(http.MethodGet, "/save", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Proto", "https")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return flashCookie(rec)
	}
	if c := send("10.0.0.1:1"); c == nil || !c.Secure {
		t.Errorf("from the trusted proxy: cookie %+v, want Secure", c)
	}
	if c := send("203.0.113.9:1"); c == nil || c.Secure {
		t.Errorf("X-Forwarded-Proto from an untrusted client: cookie %+v, want not Secure", c)
	}
}

// keyEnv names the variable holding the key, hex-encoded; a variable named but
// empty, or not hex, stops the app.
func TestKeyEnv(t *testing.T) {
	t.Setenv("FLASH_TEST_KEY", strings.Repeat("ab", 32))
	collagetest.NewSite(t,
		collagetest.WithPlugins(flash.New(flash.Options{})),
		collagetest.WithPluginConfig(flash.Name, `{"keyEnv":"FLASH_TEST_KEY"}`),
	)
	for value, want := range map[string]string{"": "is empty", "nothex": "is not hex", "abcd": "at least 32 bytes"} {
		t.Setenv("FLASH_TEST_KEY", value)
		_, err := collagetest.TrySite(t,
			collagetest.WithPlugins(flash.New(flash.Options{})),
			collagetest.WithPluginConfig(flash.Name, `{"keyEnv":"FLASH_TEST_KEY"}`),
		)
		if err == nil || !strings.Contains(err.Error(), "flash:") || !strings.Contains(err.Error(), want) {
			t.Errorf("FLASH_TEST_KEY=%q: err = %v, want %q", value, err, want)
		}
	}
}

// With no keyEnv and no Key, FLASH_KEY is read; a Key set in Go, or a keyEnv
// named, wins over it.
func TestDefaultKeyEnv(t *testing.T) {
	for value, want := range map[string]string{"nothex": "FLASH_KEY is not hex", "abcd": "at least 32 bytes"} {
		t.Setenv(flash.DefaultKeyEnv, value)
		_, err := collagetest.TrySite(t, collagetest.WithPlugins(flash.New(flash.Options{})))
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "U+") {
			t.Errorf("FLASH_KEY=%q: err = %v, want %q and no byte of the key", value, err, want)
		}
	}
	t.Setenv(flash.DefaultKeyEnv, "nothex")
	collagetest.NewSite(t, collagetest.WithPlugins(flash.New(flash.Options{Key: key})))
	t.Setenv("FLASH_TEST_KEY", strings.Repeat("ab", 32))
	collagetest.NewSite(t,
		collagetest.WithPlugins(flash.New(flash.Options{})),
		collagetest.WithPluginConfig(flash.Name, `{"keyEnv":"FLASH_TEST_KEY"}`),
	)
	t.Setenv(flash.DefaultKeyEnv, "")
	collagetest.NewSite(t, collagetest.WithPlugins(flash.New(flash.Options{})))
	t.Setenv(flash.DefaultKeyEnv, strings.Repeat("cd", 32))
	collagetest.NewSite(t, collagetest.WithPlugins(flash.New(flash.Options{})))
}

// The key no longer goes into plugins-config.json, and maxAge is a duration
// string: both old forms now stop the app.
func TestOldConfigFails(t *testing.T) {
	for raw, want := range map[string]string{
		`{"key":"` + strings.Repeat("ab", 32) + `"}`: `unknown key "key"`,
		`{"maxAge":300}`:   "as a string",
		`{"maxAge":"1ms"}`: "at least 1s",
	} {
		_, err := collagetest.TrySite(t,
			collagetest.WithPlugins(flash.New(flash.Options{Key: key})),
			collagetest.WithPluginConfig(flash.Name, raw),
		)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

// maxAge is the cookie's Max-Age, in seconds.
func TestMaxAge(t *testing.T) {
	if c := flashCookie(get(site(t), "/save")); c == nil || c.MaxAge != 300 {
		t.Errorf("default: cookie %+v, want Max-Age 300", c)
	}
	h := collagetest.NewSite(t,
		collagetest.WithPlugins(flash.New(flash.Options{Key: key, MaxAge: collage.Duration(time.Hour)})),
		collagetest.WithRegister(collage.NewAction("save").WithPath("en", "/save").WithMethods(http.MethodGet).WithHandler(
			func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
				flash.Add(rc, flash.Info, "x")
				return collage.SeeOther("/"), nil
			}).Build()),
	).App.Handler()
	if c := flashCookie(get(h, "/save")); c == nil || c.MaxAge != 3600 {
		t.Errorf("1h: cookie %+v, want Max-Age 3600", c)
	}
}
