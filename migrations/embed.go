// Package migrations embeds the SQL schema migrations so that the ledgerd
// binary can apply them without any files on disk.
package migrations

import "embed"

// FS contains the goose migration files (*.sql) at its root.
//
//go:embed *.sql
var FS embed.FS
