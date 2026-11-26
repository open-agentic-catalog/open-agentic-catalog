# Deliver a Git-managed Catalog draft

Use Git to review authoring files and `catalogctl` to save a specific commit as an editable Catalog draft. Catalog does not need access to the source repository. Start with the [terminal walkthrough](../../documentation/src/content/docs/guides/draft-transfer.mdx#deliver-a-draft-from-git). It works with a private Catalog and a local Git repository.

## Files and prerequisites

Copy the following files from the [Git draft delivery example in the Catalog repository](https://github.com/open-agentic-catalog/open-agentic-catalog/tree/main/examples/git-draft-delivery).

| File | Use |
| --- | --- |
| `deliver.py` | Stage one committed draft and an optional Skill directory. Run offline preview, fresh delivery or recovery through an installed CLI. |
| `preview.yml` | Optional pull-request preview on a disposable Ubuntu runner. Uses a reviewed helper from the base commit and the release CLI on Linux AMD64. |
| `deliver.yml` | Optional delivery from protected pushes to `main`, using one trusted Linux runner and persistent private state. |

Use Linux, Git, Python 3.11 or later and a verified `catalogctl`. The repository's [release guide](../../documentation/src/content/docs/contributing/build-and-release.md#download-release-packages) describes CLI installation. The preview example pins the release archive's SHA-256. Review that pin when changing the release.

Use regular files committed in the resource repository for the draft and its optional Skill directory. The helper preserves executable modes and ignores worktree edits. It refuses selected symbolic links, submodules and Git LFS pointers.

## Enable the optional Actions examples

1. Protect the resource repository's `main` branch. Install the reviewed helper there at `automation/catalog/deliver.py`. Copy `preview.yml` into `.github/workflows/`. Each pull request that changes `resources/agent.json` previews its proposed commit on a disposable runner.
2. Install the same reviewed helper and verified CLI on a trusted Linux runner at `/opt/oac/deliver.py` and `/opt/oac/catalogctl`. Give the runner account a persistent state directory at `/var/lib/catalog-drafts/agent` (0700). Mount its Catalog service access token at `/run/secrets/catalog-author` (0600). The runner needs network access to GitHub and the destination Catalog. For private TLS roots, add `--ca-file /absolute/path/to/ca.pem` to the delivery command.
3. Put only that runner in the `catalog-draft-delivery` group. Restrict the group to the resource repository's `deliver.yml` workflow on `refs/heads/main`. A runner label or job condition alone cannot restrict who schedules jobs. If the runner group cannot enforce these restrictions, use the terminal walkthrough. [GitHub runner access](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)
4. Set the GitHub repository variable `CATALOG_DRAFT_REPOSITORY` to the approved `owner/repository`, matching the casing of `github.repository`. Configure the destination settings below in the **delivery runner service environment**. This keeps the destination IDs out of GitHub step logs. Then copy `deliver.yml` into `.github/workflows/`.
5. After the first delivery, read the Resource ID from its private `result.json`. Set `CATALOG_RESOURCE_ID` before delivering another Version. If the first delivery created a Listing, set its ID as well. Restart the runner service between jobs if it loads its environment at startup. Keep the same state directory and concurrency group across Versions.

The templates start with an Agent file that has no Listing metadata. For the private Listing in the terminal walkthrough, add an example `--publisher-id` to preview and set the real `CATALOG_PUBLISHER_ID` on the delivery runner. To omit a supplied Listing, pass `--content-only` in both workflows and leave the Publisher and Listing settings empty. For a Skill with a local package, add its files to both workflows' path filters and pass the same `--package-directory` mapping to both commands.

When switching to later Versions, change the PR preview from `--namespace-id` to `--resource-id 00000000-0000-7000-8000-000000000002`. For an existing Listing, add an example `--listing-id` and keep the example `--publisher-id`. This previews the same commands as delivery while keeping the real destination IDs on the trusted runner.

| Delivery runner setting | Value |
| --- | --- |
| `CATALOG_URL` | Destination Control API origin, for example `https://catalog.example.org`. |
| `CATALOG_NAMESPACE_ID` | Exact destination Namespace UUID. |
| `CATALOG_RESOURCE_ID` | Leave empty for creation. For a new Version, use the existing Resource UUID. |
| `CATALOG_PUBLISHER_ID` | Supply the destination Publisher UUID only when including Listing metadata. |
| `CATALOG_LISTING_ID` | Optional exact Listing UUID when delivering a new Version with Listing metadata. |

The preview job downloads the pinned CLI and fetches the proposed commit from GitHub. The CLI then validates the selected files offline. For a fresh delivery, the trusted runner fetches the protected push's exact `after` commit into a temporary bare repository and previews it again. Both jobs treat the proposed files as data. Only the delivery runner receives a Catalog credential. Catalog checks the service account's current permissions and validates the content when the helper applies the draft.

## Recover an interrupted delivery

Inspect the private directory for the original commit. `delivery.json` records Git selection, CLI build identity, destination options and the input digest. `inputs/` retains the selected bytes and executable modes. `receipt.json` belongs to the CLI. `result.json` contains the last successful acknowledgement. On failure, inspect `diagnostic.txt` and any `last-failure.json` for blocked validation, acknowledged steps or an uncertain response. Keep command results and receipts out of shared logs, artifacts and Git.

A GitHub rerun selects `resume`. Supply the same original commit, binding and options, even if the branch or runner settings have changed since the first attempt. If the settings changed, use the terminal recovery command with the original values. The current token must resolve to the original membership. A completed resume checks the recorded revisions under current access. Private evidence reads are audited.

One unfinished attempt blocks a different commit in the same state directory. Use the last observed result to choose the next action.

| Last observed result | Operator action |
| --- | --- |
| The API response was lost and the original receipt exists. | Resume the original commit with its retained inputs, command keys and ETags. |
| Progress is missing or corrupt, or the CLI refuses replay. | Stop and inspect the destination. If a command reservation remains open, follow the [idempotency repair guide](../../documentation/src/content/docs/operations/idempotency-repair.mdx). |
| Validation blocked delivery after some writes committed. | Identify the acknowledged Resource and Version. Reconcile the partial import before choosing an unused Version label for a corrected delivery. Retain the old directory as evidence. |

Back up the state directory with access limited to the operator. Once an operator has reconciled a terminal failure, they can explicitly select a new directory for a corrected delivery. Starting a fresh apply cannot resolve an uncertain command.

Actions can replace a pending run when another push arrives. The example allows an active import to finish. A displaced run that never started has no import to resume. For a fresh delivery of an older commit, use an independently retained authentic push event for that exact protected tip with `--push-event /private/path/to/push.json`. Never accept that file from the proposed branch. Otherwise deliver the current approved tip. Git ancestry alone does not establish that a commit was a protected tip. Deleting or reverting a file in Git does not withdraw a Catalog release.

## Test locally

Run `make git-draft-delivery-check` for workflow lint, the actual preview and delivery shell commands using local Git, and input selection with the real CLI. Run `make test-process-postgres` for authenticated delivery, lost-response recovery, independent review and publication checks against an owned disposable Catalog. These checks do not dispatch a workflow, register a runner or require a public Catalog. GitHub branch protection, runner access and permissions must be configured on the operator's account. A local Actions emulator does not establish those controls.
