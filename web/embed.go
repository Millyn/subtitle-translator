package web

import _ "embed"

//go:embed subtitle.html
var SubtitleHTML []byte

//go:embed models.html
var ModelsHTML []byte

//go:embed dashboard.html
var DashboardHTML []byte

//go:embed editor.html
var EditorHTML []byte

//go:embed debug.html
var DebugHTML []byte
