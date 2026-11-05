# Issue and pull request templates

Use the smallest template that fits the work. Keep reports concise, use synthetic examples, and remove credentials and private Catalog data. Follow the [security policy](../SECURITY.md) for suspected vulnerabilities and the [contribution guide](../CONTRIBUTING.md) for verification requirements.

These Markdown templates provide prompts, not required form fields. Replace placeholders and remove sections that do not apply. State unknowns directly; a reporter does not need to diagnose a defect or propose its implementation. In PR verification, give actual results or write "Not run" with a reason.

## Issues

| Template | Use for |
| --- | --- |
| [Bug](ISSUE_TEMPLATE/01-bug.md) | Unexpected behavior with reproduction steps, expected results, and environment details. |
| [Feature](ISSUE_TEMPLATE/02-feature.md) | A user/operator need, proposed outcome, and acceptance criteria. |
| [Epic](ISSUE_TEMPLATE/03-epic.md) | A larger outcome with stories, linked work, dependencies, and verification. |
| [Documentation](ISSUE_TEMPLATE/04-documentation.md) | Incorrect guidance, missing instructions, broken links, or presentation problems. |
| [Testing](ISSUE_TEMPLATE/05-testing.md) | Test failures, missing assertions, or qualification work with explicit evidence and scope. |
| [Maintenance](ISSUE_TEMPLATE/06-maintenance.md) | Dependencies, CI, analysis tooling, packaging, or operations. |

The chooser also permits blank issues. Templates set no labels, assignees, or repository issue types. The security contact links to the policy, which explains the private reporting prerequisites. [GitHub's issue template guide](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/configuring-issue-templates-for-your-repository) describes the supported metadata and chooser configuration.

## Pull requests

The [default template](PULL_REQUEST_TEMPLATE.md) covers features, maintenance, and general changes. Remove sections that do not apply and record only verification that actually ran.

Choose an alternate by setting the `template` query parameter on the pull request creation URL. Use `?template=bug_fix.md` when there is no query string, or append `&template=bug_fix.md` to an existing one. If a `template` parameter is already present, replace its value. The directory alone does not create an automatic PR template chooser. See [GitHub's template selection documentation](https://docs.github.com/en/pull-requests/reference/using-query-parameters-to-create-a-pull-request).

| Alternate template | Query value |
| --- | --- |
| [Bug fix](PULL_REQUEST_TEMPLATE/bug_fix.md) | `template=bug_fix.md` |
| [Documentation](PULL_REQUEST_TEMPLATE/documentation.md) | `template=documentation.md` |
| [Testing](PULL_REQUEST_TEMPLATE/testing.md) | `template=testing.md` |

GitHub uses these files from the default branch. They add no workflow triggers, required checks, labels, or hosted settings. The [existing workflow guide](README.md) describes the separate manual Actions selections.
