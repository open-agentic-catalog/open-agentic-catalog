# Local PostgreSQL bootstrap

[init.sql](init.sql) is mounted by the development Compose database and runs only when PostgreSQL initializes a fresh data directory. It creates local API and worker LOGIN roles with deliberately distinct RLS authority and installs the optional vector extension available in the fixture image. Its fixed passwords are development fixtures, not production credentials.

This script does not apply Catalog schema migrations or reconcile runtime table privileges. The separate `catalogctl migrate apply` workflow uses the schema owner and the [embedded migration bundle](../../migrations/README.md). Existing database volumes do not rerun initialization merely because this script changes.

Production operators supply PostgreSQL 18 with `pgcrypto` and the identities described in the [database-role runbook](../../documentation/src/content/docs/operations/database-roles.md). Core Catalog behavior does not require pgvector, and installing it does not enable semantic search.

Use the owned `make test-functional` workflow for packaged database/bootstrap changes and `make test-integration` for repository/migration behavior. Do not delete a retained development volume to test initialization; use disposable test resources. The [local development guide](../../documentation/src/content/docs/guides/local-development.mdx) explains retained Compose data and deliberate reset.
