# Third-party notices

vless2surge embeds the unmodified upstream sing-box Core as a Go dependency. The exact source pin is recorded in `go.mod` and every release `BUILDINFO.txt`; its license text is preserved in [`LICENSES/sing-box.txt`](LICENSES/sing-box.txt).

vless2surge is an independent project and must not imply association with or endorsement by the sing-box project or its authors.

`make release VERSION=<version>` generates three additional traceability files beside the four binaries:

- `dist/THIRD_PARTY_NOTICES.txt`: the Go toolchain license and the license and notice files of every Go module linked into any supported target;
- `dist/SHA256SUMS`: SHA-256 checksums for all release binaries;
- `dist/BUILDINFO.txt`: target architecture, Go toolchain, embedded sing-box module version and binary size.

The generated inventory is evidence collection, not legal advice. The project is distributed under the root [`LICENSE`](LICENSE), which follows the same GPL-3.0-or-later terms and additional naming/association restriction used by the embedded sing-box Core. Every release must continue to preserve upstream notices and provide the corresponding source code.
