# Upstream provenance

- Source repository: https://github.com/Azure-Samples/documentdb-samples
- Exact source revision: [f5a2675bbf349737234d0391e3ebee24b260e06f](https://github.com/Azure-Samples/documentdb-samples/commit/f5a2675bbf349737234d0391e3ebee24b260e06f)
- Source directory: [ai/vector-search-agent-go](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/vector-search-agent-go)
- Extraction date: 2026-09-30
- License: original Microsoft MIT notice preserved verbatim in [LICENSE.md](LICENSE.md)
- History model: independently maintained source snapshot, not a GitHub fork or ownership transfer

## Standalone adaptations

Source is at repository root. The README is rewritten for that layout, and placeholder-only configuration/data setup replaces reliance on the parent checkout.
- Adapted source file: `.env.example`
- Adapted source file: `README.md`
- Adapted source file: `go.mod`

The Go module path changes from `github.com/Azure-Samples/documentdb-samples/ai/vector-search-agent-go` to `github.com/Azure-DocumentDB/vector-search-agent-go`, and the `internal/...` import paths follow it. The original extraction retained dependency versions; the 2026-10-12 repair below supersedes that choice. The default `DATA_FILE_WITHOUT_VECTORS` path is repository-local.

Hotel fixtures are not redistributed: [data/README.md](data/README.md) pins the original input locations and checksums. Saved output, Learn-only quickstart pages, and devcontainer automation are omitted. Shared infrastructure and root monorepo CI are not copied.

The original extraction did not intentionally change dependencies or vector/agent algorithms. The repair described below now does. The Go agent module namespace and local data/environment-file paths are adjusted where necessary. Invalid monorepo-only npm entry points are removed rather than advertised as working commands.

## Maintenance

There is no automatic synchronization. Review upstream changes against this pinned baseline, apply only relevant fixes, retain notices, and record the imported revision and local adaptations here.
Issue/PR history is not transferred. The maintainers of the repository hosting this extraction own its downstream fixes; contribute reusable fixes upstream under its contribution policy.

## Downstream repair — 2026-10-12

Starting from downstream main `5fa40a931658359535f4b5a4f9c212e00484dde3`, this repair modernizes to patched Go 1.26.9, maintained MongoDB driver v2, Azure Identity and OpenAI SDK releases. It corrects Azure API-key headers and per-role API versions; adds explicit native/Entra auth, destination/TLS/token guards, fail-closed lifecycle and bounded inference; replaces database drops/unscoped inserts with tagged deterministic insert-only seeding and narrow cleanup; and implements a strict planner/tool/synthesis transcript. Process environment is explicit (no implicit dotenv load). Permanent offline SDK/driver contracts, a separately gated genuine Azure test, read-only SHA-pinned CI, and honest test scope documentation are downstream additions. The original upstream revision and MIT notice are unchanged; these are local adaptations, not a claimed upstream import.
