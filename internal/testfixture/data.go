// Package testfixture contains small simulated data used only by tests.
package testfixture

import "embed"

//go:embed *.csv dataset.json
var Snapshot embed.FS
