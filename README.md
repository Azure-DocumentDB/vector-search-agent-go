# vector-search-agent-go

**Go**. A two-agent hotel recommendation system: a planner agent refines the query and calls a custom vector search tool against Azure DocumentDB, and a synthesizer agent writes a comparative recommendation from the results.

Standalone extraction of [the upstream sample](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/vector-search-agent-go). See [UPSTREAM.md](UPSTREAM.md) for attribution, exact revision, changes, and maintenance policy.

> This sample implements agent orchestration directly rather than using an agent framework. Upstream describes it as a reference implementation of agentic patterns, not a recommended production architecture.

## Before you run

Use **Go 1.24.3 or newer (the declared minimum)**, Git, and Azure CLI or another supported Azure Identity sign-in mechanism.
Use an existing, explicitly approved disposable Azure DocumentDB environment and an Azure OpenAI resource with three deployments: a planner chat model, a synthesizer chat model, and an embedding deployment compatible with `text-embedding-3-small` and 1536-dimensional vectors.
Your identity needs appropriate model access and a registered DocumentDB database identity with permissions for the sample operations. Network access must be configured deliberately; signing in alone does not grant database access.

**These are cloud-mutating samples, not production services or read-only demos.** They can create/drop indexes, replace/drop collections, and invoke billable models. Some programs hardcode `Hotels` or collection names; an environment-variable change alone is not a universal isolation guarantee. The agent makes chat and embedding calls on every run, so cost scales with use.

Upstream documentation differs on minimum DiskANN tiers and model/API versions. Confirm current service requirements for your selected region and tier; the shared provisioning template currently chooses M40. Do not infer live readiness from source code, saved output, or syntax/build CI.

## Setup

Commands below use PowerShell and run from this repository root. They are instructions for a separately approved run; no live run is claimed by this extraction.

### 1. Retrieve the pinned sample data

Follow [data/README.md](data/README.md). It provides explicit downloads, source links, and SHA-256 checks. This sample reads `Hotels.json` and generates its own embeddings. No data download or model call happens automatically on clone or install.

### 2. Configure the sample

Copy the placeholder-only example, then edit `.env` with your approved resource configuration. Do not commit `.env`.

```powershell
if (Test-Path -LiteralPath .env) { throw '.env already exists; edit it instead of overwriting it.' }
Copy-Item -LiteralPath .env.example -Destination .env
```

Set `USE_PASSWORDLESS=true` for Azure Identity, which is recommended, or `false` to supply `AZURE_OPENAI_API_KEY` and `AZURE_DOCUMENTDB_CONNECTION_STRING` instead. The programs load this repository-local `.env` through godotenv at run time.

For an interactive passwordless run, sign in to the intended Azure identity with `az login`; confirm the resource permissions and database identity independently. Passwordless DocumentDB access uses an OIDC token callback and requires RBAC on the cluster.

### 3. Install/build when you are ready

```powershell
go mod download
go vet ./...
```

### 4. Run only against the approved disposable environment

```powershell
go run .\cmd\upload
go run .\cmd\agent
go run .\cmd\cleanup
```

`upload` loads hotels, generates embeddings, inserts documents, and creates the vector index selected by `VECTOR_INDEX_ALGORITHM`. `agent` runs the planner and synthesizer agents against the existing store, using `QUERY` and `NEAREST_NEIGHBORS`. `cleanup` drops the sample database, so point it only at a disposable environment.

## Vector index algorithms

| Algorithm | `VECTOR_INDEX_ALGORITHM` | Cluster tier | Best for |
| --- | --- | --- | --- |
| IVF | `vector-ivf` | M10+ | Small to medium datasets, lower cost |
| HNSW | `vector-hnsw` | M30+ | High recall, fast queries |
| DiskANN | `vector-diskann` | M40+ | Large-scale datasets |

Confirm the current tier requirement for your region before selecting one.

## Shared provisioning

Provisioning remains [upstream](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/infra), not duplicated here. If you choose azd, use a separate checkout of the [upstream repository root](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/README.md), not this sample directory.
Review and harden its broad firewall/admin defaults and fallback administrator password before any deployment. No infrastructure, cloud credentials, GitHub Actions workflows, or automatic deployments are included in this extraction.

## Maintenance and license

This is a standalone downstream snapshot, not a GitHub fork of the complete monorepo. Upstream fixes must be reviewed and ported manually; update the recorded revision in [UPSTREAM.md](UPSTREAM.md) when doing so.
Dependencies and sample algorithms are retained from the pinned source. Static extraction checks do not certify dependency compatibility, authentication, deployment, or model behavior.

Sample code is Copyright (c) Microsoft Corporation, under the preserved [MIT license](LICENSE.md). Dataset source/terms remain linked in the data guide.
