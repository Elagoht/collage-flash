// A collage plugin for flash messages: a message set by an action that redirects,
// shown once by the page it redirects to, carried between them in a signed cookie.
//
// It requires collage the way any consumer does, and reaches nothing the framework
// does not offer every plugin.
module github.com/Elagoht/collage-flash

go 1.26

require github.com/Elagoht/collage v0.50.0

retract v0.1.3 // tagged at v0.1.2's commit by mistake
