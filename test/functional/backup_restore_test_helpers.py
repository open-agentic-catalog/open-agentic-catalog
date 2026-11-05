import backup_restore as backup

"""Closed evidence, all-page acceptance and fail-before-startup regressions."""

def evidence():
    return {"dumpDigest": "sha256:" + "a" * 64, "inventoryDigest": "sha256:" + "b" * 64,
            "bindingDigest": "sha256:" + "c" * 64, "dumpBytes": 1024, "tenantCount": 2,
            "verificationPages": [20, 2], "auditRows": [50, 3], "postgresVersions": [180006, 180006],
            "sourceStopped": True, "retainedState": True, "replay": True, "jobRecovered": True, "signedDelivery": True, "policyRetained": True, "tenantSettingsRetained": True,
            "pluginRetained": True, "personalItemsRetained": True, "ratingsRetained": True, "archiveStopped": True}

def page(**changes):
    value = {"formatVersion": 1, "checkSetVersion": 7, "recoveryPoint": "fixture-point", "tenantId": backup.TENANT,
             "schemaVersion": 27, "privilegeManifestVersion": 12, "traversal": "continued", "integrity": "valid",
             "processed": 7, "decodedBytes": 100, "findings": [], "assuranceBoundary": "fixture", "continuation": "next"}
    value.update(changes)
    return value

def chain(callback):
    return backup.verify_chain(callback, backup.TENANT, "fixture-point", 27, 12)
