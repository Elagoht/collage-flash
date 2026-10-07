# elagoht/flash

A collage plugin for flash messages: a message an action sets before it
redirects, shown once by the page it redirects to.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{flash.New(flash.Options{Key: key})},
})
```

Requires collage v0.50.0 or later. Register it in `Config.Plugins`: it adds a
template function, which only a plugin registered there can.

## Setting and showing one

An accepted form redirects, so a reload does not submit it twice — and the page it
redirects to has no way to say "saved" but this:

```go
func save(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
	if err := settings.Save(ctx, rc.Request); err != nil {
		return nil, err
	}
	flash.Add(rc, flash.Success, "Your changes are saved.")
	return collage.SeeOther("/settings"), nil
}
```

The layout shows what there is, and a message shown is gone:

```html
{{range flashes}}
  <p class="flash flash--{{.Kind}}" role="status">{{.Text}}</p>
{{end}}
```

`Kind` is a free string for the template to style by; `flash.Success`, `Info`,
`Warning` and `Error` are the usual ones. `Text` is text, escaped like anything
else. A handler of your own adds one with `flash.AddTo(r.Context(), kind, text)`.

A message not yet shown is kept when another is added, so a redirect through a page
that shows none does not lose it. One that is never shown is dropped after
`MaxAge` seconds.

## The cookie, and the cache

The messages travel in a cookie, `HttpOnly`, `SameSite=Lax`, and signed with
HMAC-SHA256: a reader cannot put words in the site's mouth. At most eight messages
of 300 characters are kept, which stays inside the four kilobytes a browser keeps
of a cookie.

A request carrying a message is answered with a fresh render that is neither read
from the page cache nor written to it, and marked `private, no-store`: a cached
page showing one reader's message would show it to the next. Every other request
is served as it would be without the plugin.

## The key

Set a key of at least 32 random bytes, the same on every instance and across
restarts — `openssl rand -hex 32` makes one:

```json
{ "elagoht/flash": { "key": "9f2c…" } }
```

Without one, a key is generated per process and a warning logged: a message set
just before a restart, or on another instance, is dropped.

## Configuration

```json
{
  "elagoht/flash": {
    "key": "hex-encoded, 32 bytes or more",
    "cookie": "collage_flash",
    "maxAge": 300
  }
}
```
