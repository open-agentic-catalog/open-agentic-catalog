# In-process workflow tests

This package exercises complete Catalog workflows through composed services and HTTP boundaries with test-owned dependencies. All tests use the `functional` build tag. They run in the default broad local suite and do not require PostgreSQL or a deployed environment.

| File | Coverage |
| --- | --- |
| [e2e_test.go](e2e_test.go) | Governed lifecycle, public discovery and composed workflow behavior. |
| [publication_replacement_functional_test.go](publication_replacement_functional_test.go) | Replacement publication preserves active snapshot identity, bytes and ETags; activation and explicit release withdrawal change discovery separately. |
| [api_examples_test.go](api_examples_test.go) | Reviewed API examples exercised against the service. |
| [skill_frontmatter_test.go](skill_frontmatter_test.go) | Skill frontmatter through the composed validation path. |

Run `go test -tags=functional ./internal/integration -shuffle=on -count=1` for all tests in this package. `make test-e2e` selects only `TestEndToEndAuthorReviewPublishDiscoverAndWork`; it does not run the API-example or Skill-frontmatter tests. The full package is included in `make test` and `make verify`, but excluded by `make test-unit`. Database tests belong with [platform/database](../platform/database/README.md); packaged deployment workflows live under [test](../../test/README.md).
