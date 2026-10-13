# Verification record

## Local repair verification — 2026-10-12

Baseline verified before edits: downstream `main` **5fa40a931658359535f4b5a4f9c212e00484dde3**, origin `https://github.com/Azure-DocumentDB/vector-search-agent-go.git`.

Environment: macOS arm64, **Go 1.26.9**. Dependency upgrades include MongoDB driver **v2.9.2**, OpenAI Go **v3.77.0**, Azure azcore **v1.23.3**, Azure Identity **v1.14.1**. `go.sum` is committed.

Passed locally:

- `go test -json -count=1 -timeout=2m ./...`: **36 top-level tests / 56 including named subtests**, all pass. Table-driven cases add additional assertions beyond this count.
- `go test -race -count=1 -timeout=2m ./...`: pass.
- `go vet ./...`, `go build ./...`, `go mod verify`: pass.
- `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`: **0 reachable symbol findings, 0 imported-package findings**. The module graph also contains **GO-2026-5932**, the unmaintained `golang.org/x/crypto/openpgp` package (no fixed release); that package is **not imported**. The module is needed for modern SCRAM/crypto dependencies; this advisory is disclosed, not suppressed. Verbose scanner output was reviewed.
- Actual binaries for **agent/upload/cleanup/auth-check** compiled and run with rejected scope: each exits **1** before network calls. Shared lifecycle tests also exercise embedding failure, no model call during auth, cleanup without model client, configuration preflight, and resource-close errors.
- `AGENT_LIVE_TEST= go test -tags=integration ./internal/app -run '^TestLiveAgent$' -count=1`: intentionally **fails** with missing opt-in before networking.

## What the permanent tests really exercise

**Azure REST and actual Go orchestration, with model HTTP responses mocked:** private test-only transport sends actual SDK-generated Azure requests to isolated local HTTP servers. Assertions cover exact deployment URL, independent embedding/planner/synth API versions, API-key vs Bearer headers, bodies, forced single function tool call, strict schema, assistant/tool-call ID/result conversation, fixed Entra audience and token expiry, auth-check zero inference, 401/429/500/malformed/empty/truncated output, redirect rejection, cancellation/deadline, concurrent request budget, and no retries. The real sample orchestrator runs planner → embedding → search → synthesis. The search results in that HTTP-contract test are an explicit mock; they are not claimed as real DB/model intelligence.

**Actual MongoDB driver commands, with wire responses mocked:** tests use the driver's pinned mock deployment, not a live database. They capture real update/delete/index/aggregate commands and verify deterministic `$setOnInsert`, repeat seeding, ownership/digest/foreign-field and index refusal, narrowly scoped cleanup with record snapshots and no drop commands, search pipeline ownership/path/K, failure propagation, TLS/native/Entra configuration and expiry callback. This proves client contracts, **not that every command/index kind is accepted on a deployed Azure tier**. The mock-deployment API is upstream test infrastructure and may need adjustment on future driver upgrades.

**Security boundary:** query text can only reach the embedding model, never a BSON operator/tool selector; malformed args/extra or duplicate fields/unknown tools are refused. Prompt-injection robustness of model prose is not guaranteed. Owned tags/digests guard accidental deletion in an exclusively used disposable scope, not hostile writers or multi-tenant authorization.

## Genuine managed end-to-end validation — 2026-10-12

Passed on an isolated Azure DocumentDB M30 in West US2 (one shard,128GiB, MongoDB8.0 compatibility, no HA) and an Azure OpenAI account in East US2. Both resources were restricted to the test client IP. Three synthetic hotels only; no customer data. TLS certificates/hostnames remained verified.

Models: `text-embedding-3-small`, version1,1536dimensions; planner and synthesizer `gpt-4.1-mini`, version2025-04-14. Independently configured deployments used API2024-10-21. Both native database/API-key model authentication and Entra database/Entra model authentication passed. These were genuine model requests and real database writes/queries—not a mock or precomputed fallback.

Tested implementation: `1d6b70d45f9a0c217af69a5ea65f893c67545ed6`. Each successful auth-mode run made **six real inference requests**: three seed embeddings, one planner tool-call request, one query embedding and one synthesis request. Repeated insert-only seed retained exactly three rows. The actual search results were recorded by a delegating wrapper, and synthesis was required to reference a hotel from that returned subset. The enclosing test passed after its deferred scoped cleanup, not merely after an intermediate success message.

The actual `auth-check` CLI passed both modes without inference calls. It truthfully reports that token acquisition/API-key presence alone does not prove deployment access. Wrong password, write opt-in disabled, paid opt-in disabled and unsafe database targets returned nonzero. A real unowned sentinel caused cleanup to refuse deletion; its unchanged contents were checked independently. The parent removed only its own synthetic sentinel, and the test collection was verified empty.

The gate covers COS/IVF and a three-hotel fixture. It does not certify HNSW/DiskANN, production scale, latency/recall, failover or universally factual generated prose. No request/token cap is represented as an exact monetary ceiling. Failed exploratory calls are not counted as successful gate runs.

## Independent review corrections — 2026-10-12

Three finite review findings were repaired and regression-tested: ownership comparison now preserves and compares canonical BSON recursively (including unknown nested `Address.CustomerNote` fields) with order-insensitive documents; the tool parser permits only the exact key spellings `query` and `nearestNeighbors`, rejecting case variants before any embedding/search/synthesis; the live gate records the real delegated search result and checks names from that actual subset, not every seeded name. The local grounding counterexample returns only Trail House and refuses answers mentioning only City Rooms/Beach Lodge. No extra inference requests or live mocks were added.

Cleanup snapshot predicates detect changed existing values, but do not provide whole-document equality or prevent concurrent new-field additions after reread. Exclusive writers remain required. Regression tests prove nested foreign data is refused during inspect, seed, initial cleanup, and cleanup reread before any mutation. The intermediate live log is now explicitly labeled inference checks passed with cleanup/test result pending: only the enclosing test's final success proves completed cleanup.

## Managed-search boundary follow-up — 2026-10-12

The parent/operator's first genuine native/key run on `f16d6484397d78159d375c26ad90d9f2c3fe796b` passed DB authentication, three real embedding seeds, repeat seed and index creation, but **failed** in agent search with `refusing data with foreign fields or noncanonical values`. Stored-record preflight had passed. A subsequent parent-managed synthetic 3D/no-model probe proved that `$$ROOT` contains an extra `__cosmos_meta__` document while every original stored field remains equal; there were no other field differences. This establishes the aggregate-only metadata cause without further paid inference. The failed run is not counted as live success; cleanup did not report an error.

The repair no longer treats aggregation `$$ROOT` as a canonical stored record. Search explicitly projects only owned ID and score, rejects missing/nonfinite/duplicate/unowned rankings, then performs one ordinary exact-ID+owner read per returned result and applies the **unchanged strict recursive ownership guard**. Only this independently validated stored hotel enters synthesis. This costs bounded DB reads, **no additional model calls**, and preserves all nested-foreign-data mutation protections. New mock-driver regressions cover altered aggregate representation, exact scoped hydration, foreign nested edits at hydration, invalid scores/IDs and duplicate results. Both genuine live authentication paths were rerun successfully after this fix, as recorded above.

The permanent search wire mock includes the specifically reproduced `document.__cosmos_meta__` document. It is never used as hotel data because production projects only ID/score and hydrates the canonical stored record. A separate regression proves an actual stored `__cosmos_meta__` field is still refused by InspectOwned and cleanup; no metadata-name exception was added to the stored-record validator.
