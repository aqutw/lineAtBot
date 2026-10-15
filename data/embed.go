package data

import "embed"

// Snapshot contains the user-provided historical OHLC dataset and its provenance.
//
//go:embed *.csv dataset.json
var Snapshot embed.FS
