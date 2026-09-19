package refund

import (
	_ "embed"
	"html/template"
	"net/http"
)

//go:embed page.html
var pageSource string

// page is the HTML a merchant opens. With the deployment's assets it is a
// shell the script fills; without them it shows what the state holds, and
// says that nothing here can sign.
var page = template.Must(template.New("page").Parse(pageSource))

// pageData is what the template is given.
type pageData struct {
	// State is nil for a page that was not found.
	State  *State
	Assets bool
}

// writePage writes the page under status. The template escapes what it
// prints, and the state carries nothing of the merchant's metadata.
func writePage(w http.ResponseWriter, status int, state *State, assets bool) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	return page.Execute(w, pageData{State: state, Assets: assets})
}
