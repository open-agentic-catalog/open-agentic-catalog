//! Isolated codec checks for editable draft inputs and binary evidence.

use super::*;

/// Draft modes retain authored strings, including unfinished editor content.
#[test]
fn draft_input_modes_select_exact_variants_and_preserve_editor_values() {
    let document = "{\"unknown\":9007199254740993,\"unfinished\":";
    for mode in ["data", "content"] {
        let wire = json!({"mode": mode, "text": document});
        let input: CatalogDraftRepresentationInput = serde_json::from_value(wire.clone()).unwrap();
        match &input {
            CatalogDraftRepresentationInput::CatalogDraftDocumentInput(value) => {
                assert_eq!(
                    value.mode,
                    if mode == "data" {
                        CatalogDraftDocumentInputMode::Data
                    } else {
                        CatalogDraftDocumentInputMode::Content
                    }
                );
                assert_eq!(value.text, document);
            }
            _ => panic!("Document mode selected another draft variant"),
        }
        assert_eq!(serde_json::to_value(input).unwrap(), wire);
    }

    let wire = json!({"mode": "url", "url": "https://"});
    let input: CatalogDraftRepresentationInput = serde_json::from_value(wire.clone()).unwrap();
    match &input {
        CatalogDraftRepresentationInput::CatalogDraftURLInput(value) => {
            assert_eq!(value.mode, CatalogDraftURLInputMode::Url);
            assert_eq!(value.url, "https://");
        }
        _ => panic!("URL mode selected another draft variant"),
    }
    assert_eq!(serde_json::to_value(input).unwrap(), wire);

    let handoff = "{\"git\":{\"repository\":\"";
    let wire = json!({"mode": "skill-package", "handoff": handoff});
    let input: CatalogDraftRepresentationInput = serde_json::from_value(wire.clone()).unwrap();
    match &input {
        CatalogDraftRepresentationInput::CatalogDraftSkillPackageInput(value) => {
            assert_eq!(value.mode, CatalogDraftSkillPackageInputMode::SkillPackage);
            assert_eq!(value.handoff, handoff);
        }
        _ => panic!("Skill package mode selected another draft variant"),
    }
    assert_eq!(serde_json::to_value(input).unwrap(), wire);

    let wire = json!({
        "mode": "plugin-archive",
        "url": "",
        "expectedDigest": "sha256:unfinished"
    });
    let input: CatalogDraftRepresentationInput = serde_json::from_value(wire.clone()).unwrap();
    match &input {
        CatalogDraftRepresentationInput::CatalogDraftPluginArchiveInput(value) => {
            assert_eq!(
                value.mode,
                CatalogDraftPluginArchiveInputMode::PluginArchive
            );
            assert_eq!(value.url, "");
            assert_eq!(value.expected_digest.as_deref(), Some("sha256:unfinished"));
        }
        _ => panic!("Plugin archive mode selected another draft variant"),
    }
    assert_eq!(serde_json::to_value(input).unwrap(), wire);

    let skill = "---\nname: unfinished\n";
    let wire = json!({
        "mode": "plugin-preparation",
        "manifest": "{\"name\":",
        "mcp": "{\"mcpServers\":",
        "skills": [skill],
        "sourceFiles": [
            {"path": "skills/example/SKILL.md", "retained": false},
            {"path": ".mcp.json", "retained": true}
        ]
    });
    let input: CatalogDraftRepresentationInput = serde_json::from_value(wire.clone()).unwrap();
    match &input {
        CatalogDraftRepresentationInput::CatalogDraftPluginPreparationInput(value) => {
            assert_eq!(
                value.mode,
                CatalogDraftPluginPreparationInputMode::PluginPreparation
            );
            assert_eq!(value.manifest, "{\"name\":");
            assert_eq!(value.mcp, "{\"mcpServers\":");
            assert_eq!(value.skills.len(), 1);
            assert_eq!(value.skills[0], skill);
            let files = value.source_files.as_ref().unwrap();
            assert_eq!(files.len(), 2);
            assert_eq!(files[0].path, "skills/example/SKILL.md");
            assert!(!files[0].retained);
            assert_eq!(files[1].path, ".mcp.json");
            assert!(files[1].retained);
        }
        _ => panic!("Plugin preparation mode selected another draft variant"),
    }
    assert_eq!(serde_json::to_value(input).unwrap(), wire);
}

/// Incomplete or mixed editor modes must not silently select another branch.
#[test]
fn draft_input_rejects_missing_required_and_other_mode_fields() {
    for (wire, required, other_field, other_value) in [
        (
            json!({"mode": "data", "text": ""}),
            vec!["mode", "text"],
            "url",
            json!("https://example.com/card"),
        ),
        (
            json!({"mode": "content", "text": "# Unfinished\n"}),
            vec!["mode", "text"],
            "handoff",
            json!("{}"),
        ),
        (
            json!({"mode": "url", "url": ""}),
            vec!["mode", "url"],
            "text",
            json!("must not be dropped"),
        ),
        (
            json!({"mode": "skill-package", "handoff": ""}),
            vec!["mode", "handoff"],
            "packageData",
            json!("AAEC/w=="),
        ),
        (
            json!({"mode": "plugin-archive", "url": ""}),
            vec!["mode", "url"],
            "manifest",
            json!("{}"),
        ),
        (
            json!({
                "mode": "plugin-preparation",
                "manifest": "",
                "mcp": "",
                "skills": []
            }),
            vec!["mode", "manifest", "mcp", "skills"],
            "url",
            json!("https://example.com/archive.zip"),
        ),
    ] {
        let mode = wire["mode"].as_str().unwrap();
        assert!(
            serde_json::from_value::<CatalogDraftRepresentationInput>(wire.clone()).is_ok(),
            "Valid {mode} editor control was refused"
        );
        for name in required {
            let mut missing = wire.clone();
            missing.as_object_mut().unwrap().remove(name);
            assert!(
                serde_json::from_value::<CatalogDraftRepresentationInput>(missing).is_err(),
                "{mode} accepted missing required field {name}"
            );
        }
        let mut mixed = wire.clone();
        mixed[other_field] = other_value;
        assert!(
            serde_json::from_value::<CatalogDraftRepresentationInput>(mixed).is_err(),
            "{mode} dropped the other mode's {other_field} field"
        );
        let mut unknown = wire;
        unknown["mode"] = json!("unsupported-editor-mode");
        assert!(
            serde_json::from_value::<CatalogDraftRepresentationInput>(unknown).is_err(),
            "Unknown mode fell through to a known branch"
        );
    }
}

/// Binary package decoding refuses damaged bytes and non-string wire values.
#[test]
fn skill_package_data_rejects_invalid_base64_and_wrong_wire_types() {
    let wire = json!({
        "role": "primary",
        "mediaType": "application/agent-skills+zip",
        "packageData": "AAEC/w==",
        "handoff": {
            "git": {
                "repository": "https://example.com/skills.git",
                "commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                "directory": "skills/example"
            }
        }
    });
    let valid: RepresentationInput = serde_json::from_value(wire.clone()).unwrap();
    assert_eq!(
        valid.package_data.as_deref(),
        Some([0u8, 1, 2, 255].as_slice())
    );
    for invalid in [
        json!("not*base64"),
        json!("AAEC/w="),
        json!(42),
        json!(false),
        json!({"bytes": [0, 1, 2, 255]}),
    ] {
        let mut changed = wire.clone();
        changed["packageData"] = invalid;
        assert!(
            serde_json::from_value::<RepresentationInput>(changed).is_err(),
            "Typed packageData accepted damaged base64 or a non-string value"
        );
    }
}
