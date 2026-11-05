# Reusable draft file fixtures

`cases.json` supplies the same UTF-8 input to the Go codec and browser guard. Each entry contains a name, document text and expected file validity. Document text remains a string so invalid JSON and duplicate keys survive fixture decoding.

The fixtures cover all four Resource kinds, every input mode, incomplete Listing fields, unknown native fields, exact numbers and signed native text. Rejection cases cover closed controls, version selection, source identities, inactive inputs, malformed JSON and Unicode. Size tests construct large values in memory instead of retaining large files here.

File validity does not establish native validity, command readiness or authority. Native text and extension editor text can remain incomplete. The existing browser JSON Save parser still rejects unsafe numbers. Go CLI submission retains its existing lossless path.

Run `go test ./internal/draftfile` from the repository root. Run `npm exec -- vitest run src/views/draft-file.test.ts src/views/json-input.test.ts` from `catalog-ui/`. These selections use no application binary, listener or database.
