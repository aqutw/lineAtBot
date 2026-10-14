package data

import "embed"

// Snapshot contains explicitly simulated seed data, never real market quotes.
//
//go:embed *.csv
var Snapshot embed.FS
