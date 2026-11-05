---
name: change-planning
description: Plan Open Agentic Catalog features and changes spanning domain, API, persistence or deployment boundaries. Use for requested design or implementation plans; routine local edits and test-only runs do not need this workflow.
---

# Change planning

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md` and use the documentation index (`documentation/README.md`) to find the affected sources. Preserve the requested outcome and scope: a plan-only request produces a plan; an implementation request continues into the authorized work after planning.

## Establish the current behavior

Inspect the working tree and relevant existing changes. Start with implementation status (`CONTEXT.md`) for runnable behavior, `DESIGN.md` for architecture, and the owning package, contract and tests. Use components (`documentation/src/content/docs/architecture/components.mdx`) to locate ownership and trace actual callers. Read only the applicable architecture chapter or ADR; a design constraint does not prove a feature is implemented.

When local `todo/` exists, check its active tracker (`todo/todo.md`), closed work (`todo/done.md`) and deferred proposals (`todo/deferred.md`) before proposing another implementation. These files are gitignored and optional. Otherwise, use the current request and maintained contracts. Reuse completed work and existing fixtures. A deferred proposal or a reference product's behavior is not an accepted Catalog requirement.

Distinguish a selected research direction, an accepted architecture decision and implemented capability. A superseded alternative remains historical evidence, not a parallel implementation plan or prerequisite. Resolve pending decisions before claiming their behavior is accepted or runnable.

## Map the smallest coherent change

Identify the user-visible before/after behavior and the owning domain transition. Trace affected contracts through callers and adapters: HTTP, CLI/UI, persistence, workers and configuration only where relevant. Check authorization and tenant predicates, immutable evidence, command replay, audit/outbox atomicity, cancellation and work bounds along that path. Keep domain decisions in their owning package; use an existing port unless a concrete caller and implementation require another.

For each affected area, identify the source to change, the existing behavior or seam to reuse, and the acceptance assertion. Distinguish a genuine missing assertion from an already covered case. Do not add a framework, compatibility layer or production abstraction solely to simplify a plan or increase coverage. Use an ADR only for a durable architecture choice with meaningful alternatives; follow the current compatibility boundary in AGENTS.md.

## Sequence and verify

Group work into independently reviewable behavior changes with their contract, tests and documentation kept together. Order shared prerequisites before dependent work and expose unresolved assumptions that could change the design. Scale the plan to the task; a small change does not need a separate planning document or batches.

Select acceptance checks from post-code verification (`.agents/skills/post-code-verification/SKILL.md`), and test cases from unit testing (`.agents/skills/unit-testing/SKILL.md`) or functional testing (`.agents/skills/functional-testing/SKILL.md`). Preserve explicit unit-only limits and identify any real-boundary evidence still needed. Reuse Make targets and owning harnesses; do not turn a targeted change into a mandatory full environment matrix.

When tracking is requested or an existing local item owns the work, update that item using the local tracker rules (`todo/README.md`). Keep its scope and acceptance criteria in one place. Do not require or create local trackers for ordinary planning. If local `continue.md` exists, update its concise handoff when the implementation sequence changes. Do not require or create that optional file for planning. Reader procedures and dated verification evidence have separate owners.

Report the chosen approach, affected areas, batches when useful, acceptance checks and material unknowns. Continue authorized implementation without inserting another approval step. Plan or review scope alone does not authorize implementation, retained-data changes or distribution.
