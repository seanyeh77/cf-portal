// Package webui embeds the built front end.
package webui

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets is the built front end (portal.html, admin.html, assets/...).
var Assets fs.FS = func() fs.FS {
	sub, err := fs.Sub(dist, "dist/app")
	if err != nil {
		panic(err)
	}
	return sub
}()

const marker = "<!--INIT-->"

const fallback = `<!doctype html><html lang="zh-Hant"><head><meta charset="utf-8"><title>SDC Services</title></head>
<body><p>前端尚未建置，請執行 npm run build。</p>` + marker + `</body></html>`

// Page returns an HTML entry (portal or admin) with data injected as the
// initial state. JSON encoding escapes <, > and &, so the data cannot close
// the script element.
func Page(name string, data any) []byte { return Render(Assets, name, data) }

// Render is Page for an arbitrary file system.
func Render(fsys fs.FS, name string, data any) []byte {
	html, err := fs.ReadFile(fsys, name+".html")
	if err != nil {
		html = []byte(fallback)
	}
	if data == nil {
		return html
	}
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte("{}")
	}
	tag := append([]byte(`<script id="cfp-init" type="application/json">`), b...)
	tag = append(tag, []byte(`</script>`)...)
	return bytes.Replace(html, []byte(marker), tag, 1)
}
