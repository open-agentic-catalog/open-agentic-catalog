# SonarQube screenshot

`project-overview.png` illustrates a completed local analysis. The [testing guide](../../src/content/docs/contributing/testing.md#scan-and-import-coverage) owns the reader procedure. The [SonarQube harness](../../../test/sonarqube/README.md) owns service, scanner, credential and token cleanup.

## Refresh the image

Check whether the checkout-owned service already runs. Preserve existing data, credentials and analysis history. Use `make sonarqube-up` only when needed, then run `make sonarqube-scan`. Require `status: passed-analysis` and successful scanner/token cleanup in its report.

Open the owned loopback URL in an ephemeral browser. Read credentials privately inside the automation process. Do not print them, record a trace or save browser storage. Sign in and open the Catalog project's Overview. Verify the completed analysis and displayed quality-gate result against the report. Capture the project overview without security settings, source code or issue details.

Close the browser. Stop the service with `make sonarqube-down` only if this capture started it. Retain its volumes and credentials. Review the PNG before copying it here. Register the image in `documentation/scripts/site.mjs`, then run the documentation checks.

## Reviewed observation

The current image uses analysis `8eccbd41-658a-4ad6-beab-52ed5f80d1b9`, from `output/sonarqube/scan-scpklfze/report.json`. Analysis and scanner cleanup passed. The gate is `ERROR`, displayed as Failed, and no coverage was imported. The capture started and stopped its service without deleting retained data. These observed measures do not establish reviewed defects or replace repository tests.
