# Generated A2A binding

[a2a.pb.go](a2a.pb.go) is generated from the pinned A2A protocol; [doc.go](doc.go) supplies the maintained package introduction. Do not hand-edit the binding. Exact upstream revision, compiler/plugin versions and Google API annotation inputs are recorded in [pinned profiles](../../schemas/PROFILES.md#runtime-validation).

Catalog-specific validation belongs in [adapter/a2a.go](../adapter/a2a.go). Run `go test ./internal/adapter -shuffle=on -count=1` after an authorized regeneration to check ProtoJSON and semantic compatibility.
