# Sample data

Hotel records and embeddings are not vendored in this repository. Fetch the exact upstream inputs deliberately before a run, from the repository root.

| Local file | Pinned upstream source | SHA-256 |
| --- | --- | --- |
| `Hotels.json` | [Source](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/data/Hotels.json) | `326e72e10339a3dcc5f5ce48a63aa51f93efd3f989045c03907e55075637579a` |
| `Hotels_Vector.json` | [Source](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/data/Hotels_Vector.json) | `60c08c2cdd439444ba8601d94a1f3487f4a9ee56cd48ba201d983c96722de6e4` |

```powershell
$target = '.\data\Hotels.json'
if (Test-Path -LiteralPath $target) { throw 'Hotels.json already exists; review it before replacing it.' }
Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/Azure-Samples/documentdb-samples/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/data/Hotels.json' -OutFile $target
if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne '326e72e10339a3dcc5f5ce48a63aa51f93efd3f989045c03907e55075637579a') { throw 'Data checksum mismatch; do not run the sample with this file.' }

$target = '.\data\Hotels_Vector.json'
if (Test-Path -LiteralPath $target) { throw 'Hotels_Vector.json already exists; review it before replacing it.' }
Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/Azure-Samples/documentdb-samples/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/data/Hotels_Vector.json' -OutFile $target
if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne '60c08c2cdd439444ba8601d94a1f3487f4a9ee56cd48ba201d983c96722de6e4') { throw 'Data checksum mismatch; do not run the sample with this file.' }
```

The commands refuse to overwrite existing data and verify the downloaded bytes. They do not connect to Azure or invoke a model.
If you regenerate embeddings, the resulting file will intentionally differ from the recorded upstream checksum.

Precomputed vectors use the source model/dimensions documented upstream. The .NET vector-search project has a different vector-file blob from the shared comparison dataset; its original input is preserved by its own pinned URL.

[Upstream data notes](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/data/README.md) and [license](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/LICENSE.md) remain authoritative. These hashes identify inputs; they are not a separate dataset-rights grant. Retain applicable upstream notices when using the data.
