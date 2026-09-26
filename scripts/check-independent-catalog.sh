#!/bin/sh
set -eu

# Execute the producer's tooling with its own module graph, at exactly the
# consumer's pinned version. Do not inherit a local go.work replacement or add
# publishing-only transitive dependencies to the Center/Agent dependency graph.
export GOWORK=off
catalog_tool_version=$(go list -m -f '{{.Version}}' github.com/petauron/catalog)
test -n "$catalog_tool_version"
exec go run "github.com/petauron/catalog/cmd/catalog-check@$catalog_tool_version" "$@"
