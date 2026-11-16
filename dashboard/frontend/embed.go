package frontend

import "embed"

//go:embed index.html app.js styles.css favicon.svg vendor
var Files embed.FS
