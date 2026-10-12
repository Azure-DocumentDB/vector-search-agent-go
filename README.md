# vector-search-agent-go

A real two-stage hotel recommendation agent: Azure OpenAI **planner tool call → genuine query embedding → Azure DocumentDB vector search → Azure OpenAI synthesis**, with an explicit assistant/tool-result conversation. Orchestration is implemented directly in Go; there is no third-party agent framework or deterministic recommendation fallback.

This is a standalone downstream [upstream snapshot](UPSTREAM.md), not a production service. See [TESTED.md](TESTED.md) for the exact offline/mock versus live proof boundary.

## Requirements and safety

- **Go 1.26.9 or newer compatible patched Go**, an existing approved disposable public-Azure DocumentDB cluster, and Azure OpenAI deployments. Nothing provisions infrastructure automatically.
- Chat deployments must support Chat Completions, function tools, strict schemas, `max_tokens`, and temperature (for example `gpt-4.1-mini`). Use `text-embedding-3-small` with its default 1536 dimensions. Do not assume reasoning-only models support the same contract.
- TLS certificate/hostname verification is mandatory. Exact Azure resource origins only: no HTTP, arbitrary proxies, custom CA/TLS bypass options, connection-string destination mismatch, custom SRV options, redirects, or sovereign-cloud endpoints. Model transport ignores ambient HTTP proxy variables.
- Auth modes are **independent and explicit**: DB `native`/`entra`; model `key`/`entra`. No automatic credential-mode fallback. Entra uses Azure Identity and fixed scopes `https://ossrdbms-aad.database.windows.net/.default` (DB), `https://cognitiveservices.azure.com/.default` (models); empty/near-expiry tokens are rejected, DB expiry is propagated. RBAC/database principal registration remain the operator's responsibility.
- Only a DB named `agent_go_disposable_<lowercase_run_id>` and collection **`agent_hotels`** are allowed; `AGENT_DISPOSABLE_TARGET` must exactly match `database.collection`. An affirmative name is not proof the DB is disposable: inspect it and use a dedicated least-privileged principal/exclusive namespace.
- Writes require `AGENT_ALLOW_WRITES=true`. Seed preflights ownership, rejects foreign/modified rows and unfamiliar indexes, uses deterministic IDs and **`$setOnInsert` only**. Reruns preserve existing records and do not duplicate IDs. Changed input does not update existing data: use a fresh scope or explicit owned cleanup. Embeddings may be billed again on a seed rerun.
- Cleanup additionally requires `AGENT_CONFIRM_CLEANUP` equal to the target. It deletes only verified tagged sample IDs; **never drops a database, collection, or index**. It refuses foreign data/indexes. The residual empty namespace/index is intentional; resource retirement is manual. Ownership tags/digests are accident-prevention, not cryptographic authorization; do not concurrently share the namespace. Snapshot deletion predicates detect changed existing values, but do not prevent a concurrent new-field addition after the reread; exclusive writers are required.

## Configure

[.env.example](.env.example) is a reference, not an automatically loaded credential file. Export reviewed process environment values using your shell or secret manager. Never put secrets in command-line arguments or Git. Remove old `USE_PASSWORDLESS`, custom index/field and tuning variables: unsupported legacy settings are rejected rather than silently ignored.

Required common configuration:

```sh
export AZURE_DOCUMENTDB_HOST='<exact-cluster>.global.mongocluster.cosmos.azure.com'
export AZURE_DOCUMENTDB_DATABASENAME=agent_go_disposable_example
export AZURE_DOCUMENTDB_COLLECTION=agent_hotels
export AGENT_DISPOSABLE_TARGET=agent_go_disposable_example.agent_hotels
export AZURE_DOCUMENTDB_AUTH_MODE=entra
export AZURE_OPENAI_AUTH_MODE=entra
export AZURE_OPENAI_ENDPOINT='https://<resource>.openai.azure.com/'
export AZURE_OPENAI_EMBEDDING_DEPLOYMENT=agent-embedding
export AZURE_OPENAI_PLANNER_DEPLOYMENT=agent-planner
export AZURE_OPENAI_SYNTH_DEPLOYMENT=agent-synthesizer
export AZURE_OPENAI_EMBEDDING_API_VERSION=2024-10-21
export AZURE_OPENAI_PLANNER_API_VERSION=2024-10-21
export AZURE_OPENAI_SYNTH_API_VERSION=2024-10-21
export EMBEDDING_DIMENSIONS=1536 VECTOR_INDEX_ALGORITHM=vector-ivf
export QUERY='quiet hotel near running trails' NEAREST_NEIGHBORS=3
```

For native DB access, set `AZURE_DOCUMENTDB_AUTH_MODE=native` and privately supply `AZURE_DOCUMENTDB_CONNECTION_STRING=mongodb+srv://<user>:<encoded-password>@<exact-host>/?tls=true&retryWrites=false`; omit Entra username. Only `tls/ssl=true`, `retryWrites=false`, `authSource=admin`, `authMechanism=SCRAM-SHA-256` and a simple `appName` are accepted. For model key access set `AZURE_OPENAI_AUTH_MODE=key` and privately supply `AZURE_OPENAI_API_KEY`. For Entra, unset the alternative secret variables; `AZURE_DOCUMENTDB_OIDC_USERNAME` is optional when your database principal mapping requires it. Sign in through an approved Azure Identity mechanism; sign-in alone does not establish resource permissions.

## Commands and cost guard

```sh
go mod download
go build ./...
go run ./cmd/auth-check
```

**`auth-check` never calls embedding/chat/deployment inference paths**, even if paid opt-in is set. It proves DB ping/auth, validates model configuration, and acquires an Entra model token if selected. It **cannot prove API-key validity or model deployment/RBAC access**; that requires the separately opted-in live inference gate.

Retrieve optional 1–50 hotel records using the pinned source/checksums in [data/README.md](data/README.md). Then, only after authorizing costs and mutations:

```sh
export AGENT_ALLOW_PAID_INFERENCE=true AGENT_ALLOW_WRITES=true
export DATA_FILE_WITHOUT_VECTORS=data/Hotels.json
go run ./cmd/upload
go run ./cmd/agent
export AGENT_CONFIRM_CLEANUP="$AGENT_DISPOSABLE_TARGET"
go run ./cmd/cleanup
```

`upload` fails immediately on **any** embedding failure, and generates all embeddings before inserting anything. A later DB/index failure returns nonzero but can leave a safely rerunnable partial seed. `agent` performs exactly three model requests (planner, embedding, synthesizer), never an offline answer. Malformed/unknown/multiple tool calls, argument injection/extra fields, empty/error/truncated model output, and empty search results fail; query text never becomes BSON operators. Prompt instructions and hotel descriptions remain untrusted model input—these controls limit capabilities, not guarantee truthful or injection-proof prose.

**Costs:** upload invokes one paid embedding request per hotel (maximum 50); agent invokes two paid chat calls plus one embedding. Chat output caps are 600 tokens each; there are no automatic retries. `AGENT_MAX_MODEL_CALLS` defaults to 53 and must be 1–53; this is a request/input/output bound, **not a currency ceiling**. Query ≤2048 bytes, embedding input ≤8192 bytes, search context ≤64 KiB, and `NEAREST_NEIGHBORS` 1–20 bound usage. `AGENT_TIMEOUT` defaults to 5m (1s–10m); individual HTTP/DB calls are bounded. SIGINT/SIGTERM cancel the run; resources close using a fresh bounded context. Every entrypoint returns nonzero on failure, including disconnect errors; SDK/driver error bodies are withheld from console logs.

IVF defaults to one list for this small fixture. `vector-hnsw` and `vector-diskann` use fixed bounded parameters and need appropriate service/tier support. No index is silently dropped to switch algorithms: use a new disposable scope. Confirm current regional service requirements rather than assuming tier availability.

## Offline checks

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

Tests use isolated loopback HTTP servers through private test-only transport injection and MongoDB driver's mock wire deployment. No Azure credentials or Docker required. SHA-pinned GitHub-owned actions have read-only permissions and never run the live gate with opt-in. Dependency downloads and advisory checks require Internet, but the test suite makes no cloud service calls.

## Explicit live gate (paid and mutating)

With approved credentials/configuration above, use **only** this fixed namespace:

```sh
export AZURE_DOCUMENTDB_DATABASENAME=agent_go_disposable_live_gate
export AZURE_DOCUMENTDB_COLLECTION=agent_hotels
export AGENT_DISPOSABLE_TARGET=agent_go_disposable_live_gate.agent_hotels
export AGENT_CONFIRM_CLEANUP="$AGENT_DISPOSABLE_TARGET"
export AGENT_LIVE_TEST=1 AGENT_ALLOW_PAID_INFERENCE=true AGENT_ALLOW_WRITES=true
export VECTOR_INDEX_ALGORITHM=vector-ivf EMBEDDING_DIMENSIONS=1536 AGENT_MAX_MODEL_CALLS=6
go test -tags=integration ./internal/app -run '^TestLiveAgent$' -v -count=1 -timeout=7m
```

Run separately with DB native/model key and with DB/model Entra, unsetting incompatible credentials between modes. The gate requires an initially empty, owned collection; creates three genuine 1536D embeddings; seeds twice and verifies three rows; creates IVF; runs genuine planner/search/query-embedding/synthesis; records the actual delegated search result, verifies six inference requests and a name from that returned subset in the answer; then performs narrow cleanup even on failure. Cleanup failure fails the gate. It has no mock fallback and refuses missing opt-in before network access. If interrupted, manually rerun the scoped `cleanup` command with the exact consent. No benchmark, semantic-quality score, or production availability guarantee is implied.

## Maintenance and license

MIT Microsoft notice is preserved in [LICENSE.md](LICENSE.md). Dataset provenance/terms remain in the data guide. [UPSTREAM.md](UPSTREAM.md) records the source snapshot and downstream security/lifecycle changes. Shared infrastructure remains upstream; review its defaults separately. No production deployment, Azure resource, or secret is included here.
