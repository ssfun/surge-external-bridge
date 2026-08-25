# Third-party notices

Surge External Bridge embeds the unmodified upstream Mihomo Core as a Go dependency. The exact source pin is recorded in `go.mod` and every release `BUILDINFO.txt`; its license text is preserved in [`LICENSES/mihomo.txt`](LICENSES/mihomo.txt).

Surge External Bridge is an independent project and must not imply association with or endorsement by the Mihomo project or its authors.

`make release VERSION=<version>` generates three additional traceability files beside the four binaries:

- `dist/THIRD_PARTY_NOTICES.txt`: the Go toolchain license and the license and notice files of every Go module linked into any supported target;
- `dist/SHA256SUMS`: SHA-256 checksums for all release binaries;
- `dist/BUILDINFO.txt`: target architecture, Go toolchain, embedded Mihomo module version and binary size.

The generated inventory is evidence collection, not legal advice. Mihomo is distributed under GPL-3.0; every release must preserve its license and notices and provide the corresponding source code as required. The repository root [`LICENSE`](LICENSE) governs Surge External Bridge's own code.
