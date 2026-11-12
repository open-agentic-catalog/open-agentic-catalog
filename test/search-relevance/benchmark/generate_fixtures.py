"""Generate the 160-resource corpus and 96-query benchmark dataset deterministically."""

import json
from pathlib import Path

FAMILIES = [
    # Tuning families (16 families -> 64 resources)
    ("invoice", "Invoice reconciliation", "Compare supplier invoices with purchase orders and receipts, flag price discrepancies and prepare an accounts-payable exception report.", ["finance", "invoice"], ["Invoice reconciliation", "Check supplier invoice discrepancies"], "tune"),
    ("calendar", "Calendar scheduling", "Find overlapping availability across timezones for distributed team members and propose conflict-free meeting slots.", ["calendar", "scheduling", "productivity"], ["Find team meeting slots", "Cross-timezone schedule availability"], "tune"),
    ("inventory", "Inventory forecasting", "Analyze warehouse stock levels and historic sales velocity to recommend replenishment orders before stockouts occur.", ["inventory", "supply-chain", "warehouse"], ["Warehouse stock forecasting", "Predict replenishment needs"], "tune"),
    ("redaction", "PII redaction", "Detect and mask personal identifiers, phone numbers, and email addresses in customer transcripts before distribution.", ["privacy", "security", "redaction"], ["Mask PII in transcript", "Redact personal data"], "tune"),
    ("sql", "Read-only SQL querying", "Execute safe read-only queries against approved relational database views and format result tables without modifying data.", ["database", "sql", "query"], ["Query relational database", "Fetch read-only SQL table"], "tune"),
    ("expenses", "Expense policy audit", "Check submitted employee receipts against corporate travel and entertainment expense limits, flagging non-compliant items.", ["finance", "expenses", "policy"], ["Audit expense receipts", "Check travel expense limits"], "tune"),
    ("currency", "Currency conversion", "Retrieve historical foreign exchange reference rates and convert past financial transactions between currencies accurately.", ["finance", "forex", "currency"], ["Historical exchange rate lookup", "Convert foreign currency amounts"], "tune"),
    ("translation", "Technical translation", "Translate technical documentation and developer guides accurately between English and Spanish maintaining code syntax.", ["localization", "translation", "language"], ["Translate documentation English to Spanish", "Technical Spanish translation"], "tune"),
    ("duplicate", "Duplicate contact detection", "Identify duplicated customer profiles and contact records across CRM exports using fuzzy similarity matching.", ["crm", "data-quality", "contacts"], ["Find duplicate CRM contacts", "Fuzzy matching customer records"], "tune"),
    ("compliance", "Compliance log review", "Verify system audit logs for mandatory access review markers and generate regulatory compliance verification reports.", ["compliance", "audit", "security"], ["Verify audit log compliance", "Regulatory access review check"], "tune"),
    ("dns", "DNS record inspector", "Query and validate public DNS records (A, AAAA, CNAME, MX, TXT) across multiple nameservers to detect propagation issues.", ["network", "dns", "infrastructure"], ["Inspect DNS record propagation", "Lookup domain MX and CNAME records"], "tune"),
    ("billing", "Usage billing calculator", "Calculate monthly customer charges based on metered API usage, tier thresholds, and discount schedules.", ["billing", "finance", "metering"], ["Calculate metered usage charges", "Compute monthly billing tier"], "tune"),
    ("git", "Git branch difference inspector", "Analyze commits and changed files between two Git branches, identifying merge conflicts and stale changes.", ["git", "devtools", "vcs"], ["Compare Git branches", "Inspect commit differences between branches"], "tune"),
    ("metrics", "Prometheus metric aggregator", "Query Prometheus time-series metrics to summarize service CPU, memory, and error rates over custom time windows.", ["monitoring", "prometheus", "observability"], ["Query Prometheus service metrics", "Aggregate server CPU and memory usage"], "tune"),
    ("cloud", "Cloud resource inventory", "List and filter provisioned cloud virtual machines, storage buckets, and security groups across regions without mutations.", ["cloud", "infrastructure", "inventory"], ["List cloud virtual machines", "Inventory storage buckets and security groups"], "tune"),
    ("auth", "JWT token inspector", "Decode and validate JSON Web Token (JWT) headers, claims, and expiration timestamps without verifying cryptographic signatures.", ["auth", "security", "jwt"], ["Decode JWT token claims", "Check token expiration timestamp"], "tune"),

    # Held-out families (24 families -> 96 resources)
    ("weather", "Historical weather archive", "Retrieve historical recorded temperatures, precipitation, and wind speeds from verified meteorological observation stations.", ["weather", "climate", "archive"], ["Lookup historical station weather", "Past rainfall and temperature records"], "test"),
    ("images", "Product image labeling", "Analyze product catalog photographs to detect visible objects, dominant color palettes, and category tags.", ["vision", "images", "catalog"], ["Tag objects in product photos", "Detect colors in merchandise pictures"], "test"),
    ("contract", "Contract clause reviewer", "Review commercial vendor agreements to extract renewal notice periods, liability caps, and termination provisions.", ["legal", "contracts", "review"], ["Find contract renewal deadline", "Extract agreement liability terms"], "test"),
    ("dependencies", "Dependency vulnerability scanner", "Match declared application package manifests against public security vulnerability advisories to identify vulnerable libraries.", ["security", "vulnerability", "dependencies"], ["Scan package dependencies for CVEs", "Check library security advisories"], "test"),
    ("support", "Support ticket triage", "Classify incoming customer support tickets by severity, impacted product module, and appropriate resolution queue.", ["support", "tickets", "customer-service"], ["Classify customer ticket priority", "Route support issue by product module"], "test"),
    ("csv", "CSV schema validator", "Validate CSV tabular files against expected column data types, required constraints, and unique key rules.", ["data-quality", "csv", "validation"], ["Validate CSV column types", "Check spreadsheet uniqueness constraints"], "test"),
    ("meeting", "Meeting action extractor", "Extract agreed action items, designated owners, and completion target dates from structured meeting minutes.", ["productivity", "meetings", "notes"], ["Extract meeting action items and assignees", "Parse deadlines from meeting minutes"], "test"),
    ("release-notes", "Release notes generator", "Summarize merged pull requests and issue titles into grouped, human-readable changelog release notes.", ["devtools", "release", "documentation"], ["Generate release notes from merged PRs", "Compile changelog from commit messages"], "test"),
    ("shipment", "Shipment tracking lookup", "Retrieve current carrier transit status, checkpoint timestamps, and estimated delivery dates for shipment tracking numbers.", ["logistics", "shipping", "tracking"], ["Track carrier shipment status", "Lookup delivery checkpoint updates"], "test"),
    ("kubernetes", "Kubernetes pod inspector", "Inspect Kubernetes pod health status, restart counts, and container exit codes across cluster namespaces.", ["kubernetes", "k8s", "infrastructure"], ["Inspect Kubernetes pod health and restarts", "Check cluster container exit codes"], "test"),
    ("markdown", "Markdown link validator", "Scan Markdown files to identify broken internal relative links, missing anchor headings, and malformed image tags.", ["docs", "markdown", "linting"], ["Check Markdown broken links", "Validate document anchor references"], "test"),
    ("seo", "SEO metadata audit", "Inspect HTML web pages for title tags, OpenGraph metadata, canonical URLs, and structured schema markup.", ["seo", "web", "marketing"], ["Audit webpage SEO metadata tags", "Check OpenGraph and canonical URL tags"], "test"),
    ("backup", "Backup integrity checker", "Verify database backup archive headers, checksums, and restoration manifests without performing a full database restore.", ["database", "backup", "disaster-recovery"], ["Verify database backup checksums", "Check backup archive header integrity"], "test"),
    ("email", "Email template renderer", "Render localized transactional email notifications with variable substitution and HTML/plain-text duality.", ["email", "templates", "communication"], ["Render localized email template", "Substitute variables in HTML email"], "test"),
    ("performance", "HTTP benchmark analyzer", "Parse HTTP load test results to calculate latency percentiles (p50, p95, p99), throughput, and error rates.", ["performance", "benchmark", "latency"], ["Analyze HTTP latency percentiles", "Calculate benchmark throughput and p99"], "test"),
    ("certs", "TLS certificate monitor", "Inspect SSL/TLS server certificate chains to calculate days remaining until expiration and detect weak cipher suites.", ["security", "tls", "certificates"], ["Check TLS certificate expiration date", "Inspect SSL certificate chain validity"], "test"),
    ("network", "Network route tracer", "Measure network path hops, round-trip times, and packet drop rates to diagnose latency bottlenecks.", ["network", "traceroute", "diagnostics"], ["Trace network packet route hops", "Diagnose network latency and drop rate"], "test"),
    ("audio", "Audio transcript alignment", "Align raw speech-to-text transcripts with recorded audio timestamps to remove filler words and clean punctuation.", ["audio", "speech", "transcription"], ["Align speech transcript timestamps", "Clean punctuation in audio transcript"], "test"),
    ("secrets", "Git secret scanner", "Scan repository commit history for accidentally committed API keys, private certificates, and bearer tokens.", ["security", "secrets", "git"], ["Scan Git repo for hardcoded API keys", "Detect committed private certificates"], "test"),
    ("incident", "Incident timeline builder", "Aggregate timestamped chat messages, alert triggers, and mitigation actions into a unified postmortem incident timeline.", ["incident-response", "sre", "postmortem"], ["Build incident postmortem timeline", "Aggregate incident alerts and mitigation events"], "test"),
    ("json-schema", "JSON Schema diff validator", "Compare two JSON Schema definitions to detect breaking changes such as removed properties or tightened constraints.", ["schema", "json", "api-design"], ["Compare JSON Schema definitions for breaking changes", "Detect removed fields in JSON Schema"], "test"),
    ("terraform", "Terraform syntax linter", "Parse HCL Terraform code to check naming conventions, required provider tags, and syntax errors.", ["terraform", "iac", "devops"], ["Lint Terraform HCL configuration", "Check Terraform provider tags and syntax"], "test"),
    ("docker", "Containerfile best practice linter", "Analyze Containerfiles and Dockerfiles to detect multi-stage build issues, root user execution, and untagged base images.", ["docker", "containers", "linting"], ["Lint Containerfile for security best practices", "Check Dockerfile non-root user and image tags"], "test"),
    ("cache", "Cache hit rate monitor", "Inspect in-memory key-value cache statistics to report hit ratios, memory fragmentation, and key eviction counts.", ["cache", "redis", "performance"], ["Monitor cache hit ratio and eviction count", "Inspect in-memory cache memory stats"], "test"),
]

KINDS = ["agent", "mcp_server", "skill", "plugin"]
KIND_DESCRIPTIONS = {
    "agent": "An autonomous AI Agent that manages multi-step workflows, coordinates tasks, and provides reasoned recommendations.",
    "mcp_server": "A Model Context Protocol (MCP) server providing standard tool endpoints and context resources for client assistants.",
    "skill": "A modular Agent Skill package specifying step-by-step instructions, constraints, and validation rules for repeatable execution.",
    "plugin": "An extensible Agent Plugin bundling capabilities, configuration presets, and IDE client integration points.",
}


def build_corpus():
    documents = []
    idx = 0
    for family_key, name, base_desc, tags, rep_queries, split in FAMILIES:
        for kind in KINDS:
            doc_id = f"{family_key}-{kind}"
            identifier = f"urn:air:catalog.example.test:demo:{doc_id}"
            kind_suffix = {"agent": "Assistant", "mcp_server": "Connector", "skill": "Procedure", "plugin": "Toolkit"}[kind]
            doc_name = f"{name} {kind_suffix}"
            doc_desc = f"{base_desc} {KIND_DESCRIPTIONS[kind]}"
            
            # 120 local, 40 indexed. 80 public (true), 80 private (false). All public are local.
            # Local is 3 out of 4 (agent, mcp_server, skill local; plugin indexed for some, or evenly distributed)
            is_local = (idx % 4 != 3)  # 75% local = 120 local, 40 indexed
            is_public = is_local and (idx % 2 == 0)  # 80 public local
            
            documents.append({
                "id": doc_id,
                "identifier": identifier,
                "name": doc_name,
                "description": doc_desc,
                "tags": sorted(list(set(tags + [kind]))),
                "representativeQueries": rep_queries,
                "kind": kind,
                "family": family_key,
                "source": "local" if is_local else "indexed",
                "public": is_public,
            })
            idx += 1
    return documents


def build_queries(documents):
    doc_map = {d["id"]: d for d in documents}
    queries = []
    
    # 1. Tuning queries (32 queries across 16 tuning families)
    # 16 task queries (grade 2 for all 4 kinds in family)
    # 4 filtered queries (kind specific)
    # 4 short queries
    # 4 hard-negative queries (grade 0, family: "no-answer")
    # 2 unrelated queries (grade 0, family: "no-answer")
    # 2 partial queries (grade 1)
    
    tuning_families = [f for f in FAMILIES if f[5] == "tune"]
    for fam in tuning_families[:16]:
        f_key, name, base_desc, tags, rep, _ = fam
        # Task query
        queries.append({
            "id": f"tune-{f_key}-task",
            "split": "tune",
            "family": f_key,
            "intentFamily": f_key,
            "category": "task",
            "text": f"I need a solution for {name.lower()} to {base_desc.lower()[:80]}",
            "relevant": {f"{f_key}-{k}": 2 for k in KINDS},
            "rationale": f"Explicit match for {name} capability across all resource kinds.",
        })
    
    # 4 filtered tuning queries
    for i, f_key in enumerate(["invoice", "calendar", "sql", "redaction"]):
        kind = KINDS[i]
        queries.append({
            "id": f"tune-{f_key}-filtered",
            "split": "tune",
            "family": f_key,
            "intentFamily": f_key,
            "category": "filtered",
            "text": f"Find a {kind} for {f_key} operations.",
            "kind": kind,
            "relevant": {f"{f_key}-{kind}": 2},
            "rationale": f"Explicitly filtered to {kind} for {f_key}.",
        })
    
    # 4 short tuning queries
    for f_key in ["inventory", "expenses", "currency", "git"]:
        queries.append({
            "id": f"tune-{f_key}-short",
            "split": "tune",
            "family": f_key,
            "intentFamily": f_key,
            "category": "short",
            "text": f_key.replace("-", " ").capitalize(),
            "relevant": {f"{f_key}-{k}": 2 for k in KINDS},
            "rationale": f"Short keyword search for {f_key}.",
        })
    
    # 4 hard negatives (tuning)
    hard_neg_tune = [
        ("sql", "Execute schema DDL migration dropping a production database table.", "SQL tools are strictly read-only and disallow DDL schema mutations."),
        ("redaction", "Publish unmasked customer credit card numbers directly to social media.", "Redaction tools protect privacy and disallow leaking unmasked data."),
        ("invoice", "Automatically pay unapproved vendor invoices without human accounts payable authorization.", "Invoice reconciliation prepares exception reports and forbids automatic payment execution."),
        ("currency", "Execute high-frequency arbitrage foreign exchange currency trades.", "Currency tools provide historical rate conversion and disallow trade execution."),
    ]
    for i, (f_key, text, rat) in enumerate(hard_neg_tune):
        queries.append({
            "id": f"tune-{f_key}-hard-neg-{i}",
            "split": "tune",
            "family": "no-answer",
            "intentFamily": f_key,
            "category": "hard-negative",
            "text": text,
            "relevant": {},
            "rationale": rat,
        })
    
    # 2 unrelated (tuning)
    unrelated_tune = [
        ("deepsea", "Steer an underwater submarine robotic arm to collect deep sea hydrothermal vent samples.", "No marine robotics or oceanographic sampling capabilities exist in the catalog."),
        ("bakery", "Knead sourdough bread dough and set fermentation proofing timer.", "No culinary or bakery automation capabilities exist in the catalog."),
    ]
    for i, (f_key, text, rat) in enumerate(unrelated_tune):
        queries.append({
            "id": f"tune-unrelated-{f_key}",
            "split": "tune",
            "family": "no-answer",
            "intentFamily": f_key,
            "category": "unrelated",
            "text": text,
            "relevant": {},
            "rationale": rat,
        })
    
    # 2 partial queries (tuning)
    queries.append({
        "id": "tune-partial-finance",
        "split": "tune",
        "family": "invoice",
        "intentFamily": "invoice",
        "category": "partial",
        "text": "Help with general company accounting and paperwork verification.",
        "relevant": {f"invoice-{k}": 1 for k in KINDS} | {f"expenses-{k}": 1 for k in KINDS},
        "rationale": "General accounting paperwork is partially supported by invoice and expense audit tools.",
    })
    queries.append({
        "id": "tune-partial-infra",
        "split": "tune",
        "family": "cloud",
        "intentFamily": "cloud",
        "category": "partial",
        "text": "Help inspect system infrastructure assets and network configurations.",
        "relevant": {f"cloud-{k}": 1 for k in KINDS} | {f"dns-{k}": 1 for k in KINDS},
        "rationale": "Infrastructure asset checks are partially addressed by cloud inventory and DNS tools.",
    })

    # 2. Held-out queries (64 queries across 24 held-out families)
    heldout_families = [f for f in FAMILIES if f[5] == "test"]
    
    # 24 task queries (1 per held-out family)
    for fam in heldout_families:
        f_key, name, base_desc, tags, rep, _ = fam
        # 16 of these are selected for the LangGraph agent benchmark!
        agent_bench = f_key in [
            "weather", "images", "contract", "dependencies", "support", "csv",
            "meeting", "release-notes", "shipment", "kubernetes", "markdown",
            "seo", "backup", "certs", "secrets", "performance"
        ]
        queries.append({
            "id": f"test-{f_key}-task",
            "split": "test",
            "family": f_key,
            "intentFamily": f_key,
            "category": "task",
            "agent_benchmark": agent_bench,
            "text": f"Find a resource to {base_desc.lower()[:90]}",
            "relevant": {f"{f_key}-{k}": 2 for k in KINDS},
            "rationale": f"Direct task match for {name}.",
        })
    
    # 12 filtered test queries (across different kinds)
    for i, f_key in enumerate(["weather", "images", "contract", "dependencies", "support", "csv", "meeting", "release-notes", "shipment", "kubernetes", "markdown", "seo"]):
        kind = KINDS[i % 4]
        queries.append({
            "id": f"test-{f_key}-filtered",
            "split": "test",
            "family": f_key,
            "intentFamily": f_key,
            "category": "filtered",
            "text": f"Search for a {kind} that helps with {f_key.replace('-', ' ')} operations.",
            "kind": kind,
            "relevant": {f"{f_key}-{kind}": 2},
            "rationale": f"Explicitly filtered to {kind} for {f_key}.",
        })
    
    # 8 short test queries
    for f_key in ["backup", "certs", "network", "audio", "secrets", "incident", "terraform", "docker"]:
        queries.append({
            "id": f"test-{f_key}-short",
            "split": "test",
            "family": f_key,
            "intentFamily": f_key,
            "category": "short",
            "text": f_key.replace("-", " ").capitalize(),
            "relevant": {f"{f_key}-{k}": 2 for k in KINDS},
            "rationale": f"Short keyword query for {f_key}.",
        })
    
    # 8 hard-negative queries (held-out)
    hard_neg_test = [
        ("weather", "Forecast next week's hurricane trajectory and rainfall probability.", "Weather resource contains past historical records and explicitly excludes forward forecasting."),
        ("images", "Generate realistic synthetic artwork and fantasy 3D renders from text prompts.", "Image labeling tools analyze existing photos and do not generate images."),
        ("contract", "Represent our organization as legal counsel in federal court proceedings.", "Contract review tools summarize agreement clauses and exclude courtroom representation."),
        ("dependencies", "Execute untested malicious binary in production to analyze malware behavior.", "Dependency scanner matches CVE advisories and forbids arbitrary binary execution."),
        ("kubernetes", "Terminate all nodes in a production Kubernetes cluster immediately.", "Kubernetes pod inspector is read-only and forbids destructive node termination."),
        ("secrets", "Automatically decrypt and broadcast found API keys to a public channel.", "Secret scanner detects committed credentials and does not broadcast secrets."),
        ("backup", "Purge all historical disaster recovery backups without verification.", "Backup checker verifies archive integrity and prohibits destructive purge actions."),
        ("audio", "Synthesize fake celebrity voice audio clone from sample speech.", "Audio tool cleans transcripts and does not synthesize deepfake voice clones."),
    ]
    for i, (f_key, text, rat) in enumerate(hard_neg_test):
        queries.append({
            "id": f"test-{f_key}-hard-neg-{i}",
            "split": "test",
            "family": "no-answer",
            "intentFamily": f_key,
            "category": "hard-negative",
            "text": text,
            "relevant": {},
            "rationale": rat,
        })
    
    # 4 unrelated test queries
    unrelated_test = [
        ("astronomy", "Point the orbital space telescope toward the Andromeda galaxy.", "No space telescope positioning capabilities exist."),
        ("farming", "Irrigate the north agricultural field with automated tractor sprinklers.", "No agricultural tractor irrigation capabilities exist."),
        ("surgery", "Perform robotic laparoscopic cardiac bypass surgery.", "No medical surgical capabilities exist."),
        ("geology", "Simulate tectonic plate continental drift over fifty million years.", "No geological simulation capabilities exist."),
    ]
    for i, (f_key, text, rat) in enumerate(unrelated_test):
        queries.append({
            "id": f"test-unrelated-{f_key}",
            "split": "test",
            "family": "no-answer",
            "intentFamily": f_key,
            "category": "unrelated",
            "text": text,
            "relevant": {},
            "rationale": rat,
        })
    
    # 4 partial test queries
    queries.append({
        "id": "test-partial-devops",
        "split": "test",
        "family": "kubernetes",
        "intentFamily": "kubernetes",
        "category": "partial",
        "text": "Help with general cluster container deployment and build pipeline tasks.",
        "relevant": {f"kubernetes-{k}": 1 for k in KINDS} | {f"docker-{k}": 1 for k in KINDS},
        "rationale": "General cluster devops tasks are partially addressed by pod inspection and Containerfile linting.",
    })
    queries.append({
        "id": "test-partial-security",
        "split": "test",
        "family": "secrets",
        "intentFamily": "secrets",
        "category": "partial",
        "text": "Help audit software security posture and vulnerable components.",
        "relevant": {f"secrets-{k}": 1 for k in KINDS} | {f"dependencies-{k}": 1 for k in KINDS},
        "rationale": "Security posture auditing is partially addressed by secret scanning and dependency checking.",
    })
    queries.append({
        "id": "test-partial-content",
        "split": "test",
        "family": "release-notes",
        "intentFamily": "release-notes",
        "category": "partial",
        "text": "Help prepare customer-facing documentation and update announcements.",
        "relevant": {f"release-notes-{k}": 1 for k in KINDS} | {f"meeting-{k}": 1 for k in KINDS},
        "rationale": "Documentation announcements are partially aided by release summaries and meeting action logs.",
    })
    queries.append({
        "id": "test-partial-web",
        "split": "test",
        "family": "seo",
        "intentFamily": "seo",
        "category": "partial",
        "text": "Help inspect website health and user-facing landing page performance.",
        "relevant": {f"seo-{k}": 1 for k in KINDS} | {f"performance-{k}": 1 for k in KINDS},
        "rationale": "Website landing page health is partially addressed by SEO audits and HTTP latency tests.",
    })

    # 4 exact queries (test)
    for i, f_key in enumerate(["weather", "images", "contract", "dependencies"]):
        kind = KINDS[i]
        doc = doc_map[f"{f_key}-{kind}"]
        queries.append({
            "id": f"test-{f_key}-exact",
            "split": "test",
            "family": f_key,
            "intentFamily": f_key,
            "category": "exact",
            "kind": kind,
            "exact": True,
            "text": doc["identifier"],
            "relevant": {doc["id"]: 2},
            "rationale": f"Exact AIR identifier lookup for {doc['id']}.",
        })

    return queries


def main():
    root = Path(__file__).resolve().parent
    documents = build_corpus()
    queries = build_queries(documents)
    
    print(f"Generated {len(documents)} documents across 4 kinds:")
    for k in KINDS:
        print(f"  {k}: {sum(d['kind'] == k for d in documents)}")
    
    tune_count = sum(q['split'] == 'tune' for q in queries)
    test_count = sum(q['split'] == 'test' for q in queries)
    print(f"Generated {len(queries)} queries ({tune_count} tune, {test_count} test)")
    
    (root / "corpus.json").write_text(json.dumps(documents, indent=2) + "\n")
    (root / "queries.json").write_text(json.dumps(queries, indent=2) + "\n")
    print("Saved corpus.json and queries.json successfully.")


if __name__ == "__main__":
    main()
