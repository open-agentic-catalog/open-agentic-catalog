# Local federation credential mount

This non-secret placeholder keeps the default Compose mount valid. Point `CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY` at a protected directory outside the repository only when credential resolution is enabled.

The variable selects the host-side mount only. Also set `CATALOG_CREDENTIAL_RESOLUTION_ENABLED=true` and `CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY=/etc/open-agentic-catalog/federation/credentials` for the Compose API and worker. Merely mounting this directory does not enable a resolver or create any bindings.

A source's opaque `credentialBinding` value such as `partner-reader` selects `partner-reader.json` at the mount root. Its metadata identifies the exact tenant, normalized origin, non-secret revision, credential type, and referenced leaf filenames. A bearer or API-key binding uses `secretFile`; an mTLS binding instead uses `certificateFile` and `privateKeyFile`. These are leaf names within the mounted directory, not arbitrary paths. The [credential-binding reference](../../documentation/src/content/docs/reference/configuration.mdx#federated-source-credential-bindings) includes a non-secret metadata example and the exact size, expiry, and validation rules.

Project each binding metadata file and all referenced leaves atomically, and use a new never-reused non-secret revision whenever any credential material or constraint changes. The resolver reads each request's binding from the mount, so a complete credential-generation rotation does not require a process restart; changing resolver enablement or its directory does. Keep actual metadata, tokens, certificates, and private keys outside this example directory. Federation credentials are not OIDC verifier or webhook receiver credentials.
