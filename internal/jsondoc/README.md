# Unambiguous JSON input

[jsondoc.go](jsondoc.go) validates one complete JSON value, rejecting duplicate object member names, trailing values, malformed input and excessive nesting. Callers still enforce their own byte limits and typed/schema validation.

Use this boundary before decoders that would otherwise accept duplicate keys or preserve raw standards evidence. It deliberately does not define a domain object's allowed members or canonicalize the input.

Run `go test ./internal/jsondoc -shuffle=on -count=1`.
