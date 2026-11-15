# Reviewed visual baselines

Source-snapshot comparison updates only `api-endpoints-light.png` and `api-definitions-light.png` for 199 operations and 455 definitions. Reviewed diffs contain only those contract totals; layout, controls and dark references remain unchanged.

Reusable draft transfer refreshes only `authoring-narrow.png` and `confirmation-narrow.png`. Reviewed run `run-MFCE4d` shows the intended gaps below Namespace refresh and the AIR builder. Run `run-YgK4vE` shows only the shifted page background at the dialog's transparent top edge. The dialog's content and dimensions stay unchanged. The expanded AIR actions also use the existing wrapping action row. All other expected images retain their bytes.

Reusable draft transfer updates the two API light references for 197 operations and 443 definitions. The reviewed diffs contain only these contract totals. Source captures are in `run-XlgdYK`. Both complete comparisons pass in `run-0depBQ` and `run-u2wkJ8`.

These expected PNGs belong to the opt-in `visual.spec.ts` tests. They are not documentation screenshots. Actual, previous and diff images remain under ignored `output/playwright/ui-tests/run-*`.

The suite compares forty-six expected images across twenty-one tests. The table lists every expected image and its view.

| Image                                     | Stable view                                                                                                             |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `document-changes-light.png`              | Selected retained Skill document changes, with exact before/after lines in light desktop appearance.                    |
| `document-changes-dark-narrow.png`        | The same document changes in a 390px dark viewport, retaining readable line and change context.                         |
| `mcp-client-controls-light.png`           | Local MCP client configuration controls on a published resource, light desktop.                                         |
| `mcp-client-controls-dark-narrow.png`     | The same configuration controls at 390px with dark appearance and doubled text.                                         |
| `overview-administration-light.png`       | Human/Service lifecycle bars, exact tables, definitions, feedback categories and retained rating states, light desktop. |
| `overview-administration-dark-narrow.png` | The same synthetic Administration values at 390px in dark appearance, without horizontal overflow.                      |
| `api-endpoints-light.png`                 | Grouped endpoint section, combined filters and collapsed membership operation, light desktop.                           |
| `api-authentication-dark-narrow.png`      | Expanded membership authentication with its HTTP Bearer declaration and local scheme link, dark, 390 x 1000.            |
| `api-definitions-light.png`               | Complete-index search, kind/page controls and selected ResourceKind schema at 1440 x 1400, light.                       |
| `api-schema-dark-narrow.png`              | Declared ResourceKind type and enumeration, dark, 390 x 1000.                                                           |
| `review-filters-light.png`                | Exact Listing and state filters with explicit Apply, Clear and Refresh, light desktop.                                  |
| `review-opener-dark-narrow.png`           | Exact Submission opener and permission-independent guidance at 390 x 844, dark.                                         |
| `native-preparation-light.png`            | Collapsed Agent starting point with a separate documentation link, light desktop.                                       |
| `native-preparation-dark-narrow.png`      | Keyboard-expanded native input, version and validation guidance with a separate link at 390 x 844, dark.                |
| `listing-prepared-light.png`              | Captured saved/proposed Listing display fields and advanced disclosures, light desktop.                                 |
| `listing-prepared-dark-narrow.png`        | One complete saved/proposed description field inside the scrollable confirmation at 390 x 844, dark.                    |
| `search-light.png`                        | Reader shell and three name-first published demo cards, light, 1440 x 900 viewport.                                     |
| `search-dark-compact-narrow.png`          | The same four-kind Catalog in dark/compact appearance at 390 x 844, including all three cards.                          |
| `account-light.png`                       | Open Account panel with the synthetic Reader identity and permitted personal links, light desktop.                      |
| `account-dark-compact-narrow.png`         | The same Account panel in dark/compact appearance at 390 x 844.                                                         |
| `filters-desktop.png`                     | In-page filter panel with staged kinds/tags at desktop width.                                                           |
| `filters-narrow.png`                      | The same draft in a modal drawer at 390 x 740, with Apply reachable.                                                    |
| `access-roles-light.png`                  | Current policy administration with a synthetic custom role and built-in definitions, light desktop.                     |
| `namespaces-dark.png`                     | Administrator shell, exact display-name/slug filters and populated namespace table, dark, 1440 x 900 viewport.          |
| `authoring-narrow.png`                    | Resource form before namespace selection, light, 390 x 844; full-page capture retains the disabled submit control.      |
| `confirmation-narrow.png`                 | Resource confirmation dialog at the same narrow viewport, including both actions.                                       |
| `resource-assessment-light.png`           | Reported local validation, trust and legal-rights states before handoff, light desktop.                                 |
| `resource-assessment-dark-narrow.png`     | The same assessment at 390 x 1800, centered below the sticky header.                                                    |
| `resource-handoff-light.png`              | Shared consumer handoff on a local Skill record, light desktop.                                                         |
| `resource-handoff-dark-narrow.png`        | The same section in a 390 x 1800 viewport, with complete bounds checked below the sticky header.                        |
| `explore-kinds-light.png`                 | Local browse kinds and text labels, light desktop.                                                                      |
| `explore-kinds-dark-narrow.png`           | The same kind palette at 390 pixels, dark.                                                                              |
| `operations-jobs-light.png`               | Job filters and contextual work states, light desktop.                                                                  |
| `operations-jobs-dark-narrow.png`         | The same Job controls at 390 pixels, dark.                                                                              |
| `settings-actions-light.png`              | Settings actions, inheritance and reset controls, light desktop.                                                        |
| `settings-reset-dark-narrow.png`          | Captured Settings reset confirmation at 390 pixels, dark.                                                               |
| `about-kinds-light.png`                   | Public About text and the four decorative kind symbols, light desktop.                                                  |
| `getting-started-dark-narrow.png`         | Reader workflow and documentation links at 390 pixels, dark and compact.                                                |
| `overview-kinds-light.png`                | Local resource kinds with the shared palette and a common count scale, light desktop.                                   |
| `overview-kinds-dark-narrow.png`          | The same kind cards at 320 pixels, dark.                                                                                |
| `overview-reviews-light.png`              | Review workload, activity and age distributions, light desktop.                                                         |
| `overview-reviews-dark-narrow.png`        | The same review panel at 390 pixels, dark.                                                                              |
| `overview-operations-light.png`           | Source, Job and search-index summaries with exact tables, light desktop.                                                |
| `overview-operations-dark-narrow.png`     | The same Operations panels at 390 pixels, dark.                                                                         |
| `overview-webhooks-light.png`             | Endpoint states, retained delivery failures and separate notification counters, light desktop.                          |
| `overview-webhooks-dark-narrow.png`       | The same webhook panels at 390 pixels, dark.                                                                            |

Use the [visual comparison environment](../../../../documentation/src/content/docs/contributing/testing.md#visual-comparison-environment) documented in the testing guide. Browser/toolchain updates require renewed image review, not automatic replacement of expected results.

The test configuration fixes `en-US`, UTC, light initial theme, reduced motion and viewport; the dark tests change theme and, for narrow Catalog, density through the real Appearance control. Screenshots disable animations and hide the caret. Zero differing pixels are allowed beyond a 0.05 perceptual-color threshold, needed for slight native-select corner rasterization variation between consecutive captures. Administration context shows the real, deterministic tenant and realm IDs from development seeding. No random target IDs, dynamic masks or frozen service clocks enter these views.

Overview fixtures provide fixed response timestamps for review, Source, Job, webhook and Administration panels. Operations and webhook cases also fix browser time for response-receipt labels. These fixtures test presentation, not real delivery, indexing or sync outcomes. Separate functional journeys verify those outcomes through the actual API.

Run from the repository root after `make ui-install ui-browser-install`:

```sh
make ui-visual          # Compare; missing or changed baselines fail
make ui-visual-update   # Deliberately regenerate all forty-six expected images
```

Both targets use the private-build fixture and leave development servers/assets alone. Visual tests are excluded from `make test-ui`; they do not silently add an OS-sensitive pixel gate to ordinary functional tests. Retries stay disabled. Normal comparison never creates an expected baseline; only the explicit update command enables that behavior.

After an intentional UI or toolchain change, review each changed PNG and any prior failure diff for clipping, missing content, focus indicators, labels, theme contrast and action spacing. Then run `make ui-visual` twice without updates. Do not accept a screenshot merely because the update command passed. Functional assertions and accessibility checks remain separate requirements.

## Change evidence

Overview dashboard fixes narrow heading/action wrapping. Six reviewed dark narrow images change after the copy gains usable width. The dashboard crops also reflect vertical positioning and pixel rounding. Update `run-X6DVgm` preserves every label and value. Full comparisons pass in `run-x3Vuyi` and `run-M0qLr7`. Desktop references remain unchanged. The new desktop Content capture stays byte-identical to its manual image.

Overview dashboard adds two reviewed Administration images from `run-R6yzkj`. It refreshes two API light images for the 196-operation, 430-definition contract. The prior diffs contain only those totals. All nineteen cases pass without updates in `run-A6zKmd` and `run-auCN4Z`.

This inventory does not establish a fresh comparison run. Keep failed attempts, reviewed image changes and passing comparisons in local reports under ignored `output/`.

The guided-authoring review updates only the expanded dark preparation reference for the new optional-Listing instructions. The reviewed actual image matches the refreshed reference byte for byte. Complete visual run `run-7ioFgC` passes all nineteen cases. The other references keep their prior purposes.

Authoring layout updates three reviewed references. `authoring-narrow.png` shows required stars, the inline legend and the navigation icon. `native-preparation-dark-narrow.png` reflects the intended help wrapping. `confirmation-narrow.png` changes 135 pixels behind its rounded top edge after the form height changes. Dialog text and controls remain intact. Only those actual images replace their references; comparison results are retained in the authoring review.

Reusable draft transfer updates `authoring-narrow.png` and `confirmation-narrow.png` after reviewing the expected and actual captures in `run-AOlIbb`. The intentional change adds the compact draft-file disclosure above the form. Input focus, field widths and all existing controls remain visible. The confirmation review in `run-jCan35` shows only the shifted background through its transparent border, with unchanged dialog content and geometry. The complete visual suite verifies the remaining baselines without changes.
