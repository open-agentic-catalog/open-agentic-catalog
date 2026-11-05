"""Shared bounded acceptance of isolated restore-verification pages."""

PAGE_LIMIT = 7
MAX_PAGES = 200
MAX_DUMP = 16 << 20
ALLOWED_FINDINGS = {"RESTORE_ACTIONABLE_JOB_RETAINED", "RESTORE_UNACKNOWLEDGED_OUTBOX_EVENT",
                    "RESTORE_PENDING_INTERNAL_OUTBOX_EVENT"}
FINDING_DOMAINS = {code: "job" if code == "RESTORE_ACTIONABLE_JOB_RETAINED" else "outbox_event" for code in ALLOWED_FINDINGS}


def verify_chain(read_page, tenant, recovery_point, schema, manifest, error_type=ValueError, minimum_pages=2):
    """Consume exactly the preceding cursor; reject any bad page, even exit-zero."""
    cursor, seen, findings = "", set(), set()
    required = {"formatVersion", "checkSetVersion", "recoveryPoint", "tenantId", "schemaVersion",
                "privilegeManifestVersion", "traversal", "integrity", "processed", "decodedBytes", "findings", "assuranceBoundary"}
    for number in range(1, MAX_PAGES + 1):
        page = read_page(cursor)
        if (not isinstance(page, dict) or not required.issubset(page) or set(page) - required - {"continuation"}
                or any(type(page[key]) is not int for key in ("formatVersion", "checkSetVersion", "schemaVersion", "privilegeManifestVersion"))
                or page["formatVersion"] != 1 or page["checkSetVersion"] != 7
                or page["tenantId"] != tenant or page["recoveryPoint"] != recovery_point
                or page["schemaVersion"] != schema or page["privilegeManifestVersion"] != manifest
                or page["integrity"] != "valid" or page["traversal"] not in ("continued", "complete")
                or type(page["processed"]) is not int or not 0 <= page["processed"] <= PAGE_LIMIT
                or type(page["decodedBytes"]) is not int or not 0 <= page["decodedBytes"] <= 80 << 20
                or not isinstance(page["assuranceBoundary"], str) or not page["assuranceBoundary"]
                or not isinstance(page["findings"], list) or len(page["findings"]) > 256):
            raise error_type("backup-invalid-verification-page")
        for finding in page["findings"]:
            fields = {"code", "class", "severity", "domain", "disposition"}
            if (not isinstance(finding, dict) or not fields.issubset(finding) or set(finding) - fields - {"recordId"}
                    or any(not isinstance(value, str) or not value for value in finding.values())
                    or finding["class"] != "operational" or finding["code"] not in ALLOWED_FINDINGS
                    or finding["severity"] != "warning" or finding["domain"] != FINDING_DOMAINS[finding["code"]]):
                raise error_type("backup-unexpected-finding")
            findings.add(finding["code"])
        following = page.get("continuation", "")
        if not isinstance(following, str):
            raise error_type("backup-invalid-continuation")
        if page["traversal"] == "complete":
            if following or number < minimum_pages:
                raise error_type("backup-incomplete-pagination-exercise")
            return number, findings
        if not isinstance(following, str) or not 1 <= len(following) <= 4096 or following in seen:
            raise error_type("backup-invalid-continuation")
        seen.add(following)
        cursor = following
    raise error_type("backup-page-limit")
