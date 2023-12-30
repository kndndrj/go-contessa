# Go Contessa

[![Go Reference](https://pkg.go.dev/badge/github.com/kndndrj/go-contessa.svg)](https://pkg.go.dev/github.com/kndndrj/go-contessa)

Library for easier testing with containers.
Originally developed for testing [nvim-dbee](https://github.com/kndndrj/nvim-dbee).

Only basic functionality for now - adding fetures if needed.

## Quick Start

```go
package example_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kndndrj/go-contessa"
)

func TestExample(t *testing.T) {
	r := require.New(t)
	cm, err := contessa.New(contessa.WithTestingLogger(t))
	r.NoError(err)

	// get a free port from os
	port := contessa.GetFreePortOr(5177)

	// create a new postgres container
	ready, err := cm.StartContainer("postgres:15",
		// port binding
		contessa.WithPortBinding(port, 5432),

		// env vars
		contessa.WithEnvVariable("POSTGRES_USER", "postgres"),
		contessa.WithEnvVariable("POSTGRES_PASSWORD", "postgres"),
		contessa.WithEnvVariable("POSTGRES_DB", "postgres"),

		// startup probe and delay
		contessa.WithStartupProbe("pg_isready"),
		contessa.WithStartupProbeSkew(2*time.Second),
		contessa.WithStartupDelay(10*time.Second),
	)
	r.NoError(err)

	// Wait for the container to be set-up
	<-ready

	// connect to postgres container
	dsn := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port)
	db, err := sql.Open("pgx", dsn)
	r.NoError(err)
	defer db.Close()

	// do whatever you need with "db"...
}
```
