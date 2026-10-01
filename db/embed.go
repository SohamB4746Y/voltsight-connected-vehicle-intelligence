// Package db embeds the SQL migrations so that tools and tests apply exactly the committed schema.
package db

import "embed"

// Migrations holds the golang-migrate compatible *.up.sql / *.down.sql files.
//
//go:embed migrations/*.sql
var Migrations embed.FS
