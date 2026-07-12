package opencodeplugin

import _ "embed"

// Source is the production OpenCode plugin used by setup/install surfaces and eval.
// Runtime paths and eval switches are supplied through environment variables.
//
//go:embed plugin.js
var Source string
