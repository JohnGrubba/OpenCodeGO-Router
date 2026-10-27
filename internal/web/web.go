// Package web embeds the admin dashboard.
package web

import "embed"

//go:embed static/*
var assets embed.FS

// FS returns the embedded static asset filesystem (rooted at "static").
func FS() embed.FS { return assets }
