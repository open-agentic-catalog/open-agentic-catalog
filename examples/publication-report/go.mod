module github.com/open-agentic-catalog/open-agentic-catalog/examples/publication-report

go 1.27.3

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/open-agentic-catalog/open-agentic-catalog/sdk/go v0.1.0
)

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/oapi-codegen/runtime v1.8.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/open-agentic-catalog/open-agentic-catalog/sdk/go => ../../sdk/go
