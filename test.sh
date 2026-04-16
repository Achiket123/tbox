#!/bin/bash

# 1. Ensure dependencies are fetched
go mod tidy

# 2. Verify no import cycles
go list -f '{{.ImportPath}} {{.Deps}}' ./... | grep -v "github.com/tbox-run/tbox"

# 3. Build the project
go build ./cmd/tbox

# 4. Run static analysis
gosec ./...  # Should show only MEDIUM/LOW issues (no CRITICAL/HIGH)