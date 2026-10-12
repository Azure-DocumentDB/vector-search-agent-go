# Verification record

## Local repair verification — 2026-10-12

Baseline verified before edits: downstream `main` **5fa40a931658359535f4b5a4f9c212e00484dde3**, origin `https://github.com/Azure-DocumentDB/vector-search-agent-go.git`.

Environment: macOS arm64, **Go 1.26.9**. Dependency upgrades include MongoDB driver **v2.9.2**, OpenAI Go **v3.77.0**, Azure azcore **v1.23.3**, Azure Identity **v1.14.1**. `go.sum` is committed.

Passed locally:

- `go test -json -count=1 -timeout=2m ./...`: **33 top-level tests / 51 including named subtests**, all pass. Table-driven cases add additional assertions beyond this count.
- `go test -race -count=1 -timeout=2m ./...`: pass.
- `go vet ./...`, `go build ./...`, `go mod verify`: pass.
- `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`: **0 reachable symbol findings, 0 imported-package findings**. The module graph also contains **GO-2026-5932**, the unmaintained `golang.org/x/crypto/openpgp` package (no fixed release); that package is **not imported**. The module is needed for modern SCRAM/crypto dependencies; this advisory is disclosed, not suppressed. Verbose scanner output was reviewed.
- Actual binaries for **agent/upload/cleanup/auth-check** compiled and run with rejected scope: each exits **1** before network calls. Shared lifecycle tests also exercise embedding failure, no model call during auth, cleanup without model client, configuration preflight, and resource-close errors.
- `AGENT_LIVE_TEST= go test -tags=integration ./internal/app -run '^TestLiveAgent$' -count=1`: intentionally **fails** with missing opt-in before networking.

## What the permanent tests really exercise

**Azure REST and actual Go orchestration, with model HTTP responses mocked:** private test-only transport sends actual SDK-generated Azure requests to isolated local HTTP servers. Assertions cover exact deployment URL, independent embedding/planner/synth API versions, API-key vs Bearer headers, bodies, forced single function tool call, strict schema, assistant/tool-call ID/result conversation, fixed Entra audience and token expiry, auth-check zero inference, 401/429/500/malformed/empty/truncated output, redirect rejection, cancellation/deadline, concurrent request budget, and no retries. The real sample orchestrator runs planner → embedding → search → synthesis. The search results in that HTTP-contract test are an explicit mock; they are not claimed as real DB/model intelligence.

**Actual MongoDB driver commands, with wire responses mocked:** tests use the driver's pinned mock deployment, not a live database. They capture real update/delete/index/aggregate commands and verify deterministic `$setOnInsert`, repeat seeding, ownership/digest/foreign-field and index refusal, narrowly scoped cleanup with record snapshots and no drop commands, search pipeline ownership/path/K, failure propagation, TLS/native/Entra configuration and expiry callback. This proves client contracts, **not that every command/index kind is accepted on a deployed Azure tier**. The mock-deployment API is upstream test infrastructure and may need adjustment on future driver upgrades.

**Security boundary:** query text can only reach the embedding model, never a BSON operator/tool selector; malformed args/extra or duplicate fields/unknown tools are refused. Prompt-injection robustness of model prose is not guaranteed. Owned tags/digests guard accidental deletion in an exclusively used disposable scope, not hostile writers or multi-tenant authorization.

## Live Azure status

**Not run by this code-repair worker.** No Azure credentials were read, no cloud resource created/changed, and no paid model call made during the verification above. A successful mock is not counted as genuine model success. The parent/operator must append separate live evidence (exact code revision, chosen auth modes, models/deployments/API versions, scope, request count, cleanup outcome) before treating the live gate as passed.

The opt-in integration test is runnable as documented in [README.md](README.md). It uses only `agent_go_disposable_live_gate.agent_hotels`, a maximum six planned inference calls, three actual 1536D seed vectors, repeated idempotent seed, IVF, genuine planner/query embedding/DB search/synthesis, and cleanup. No local/mock fallback is enabled. It refuses absent consent or an initially populated/foreign target. HNSW and DiskANN are **not covered by that live gate**.

GitHub Actions execution, Linux/stable-Go matrix, live API-key/native+Entra behavior, tier capability, semantic recommendation quality, model cost totals, and performance are **not claimed by local build/mock results**. Publication/remote changes are outside this repair worker's scope.

## Independent review corrections — 2026-10-12

Three finite review findings were repaired and regression-tested: ownership comparison now preserves and compares canonical BSON recursively (including unknown nested `Address.CustomerNote` fields) with order-insensitive documents; the tool parser permits only the exact key spellings `query` and `nearestNeighbors`, rejecting case variants before any embedding/search/synthesis; the live gate records the real delegated search result and checks names from that actual subset, not every seeded name. The local grounding counterexample returns only Trail House and refuses answers mentioning only City Rooms/Beach Lodge. No extra inference requests or live mocks were added.

Cleanup snapshot predicates detect changed existing values, but do not provide whole-document equality or prevent concurrent new-field additions after reread. Exclusive writers remain required. Regression tests prove nested foreign data is refused during inspect, seed, initial cleanup, and cleanup reread before any mutation. The intermediate live log is now explicitly labeled inference checks passed with cleanup/test result pending: only the enclosing test's final success proves completed cleanup.
