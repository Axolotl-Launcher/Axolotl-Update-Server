package store

import "io/fs"

func subFS(dialect string) (fs.FS, error) { return fs.Sub(migrations, "migrations/"+dialect) }
