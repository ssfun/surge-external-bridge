# Node DNS acceptance and failure cases

Written before implementation. Verification uses the real management API, persisted gateway.json,
authenticated gateway SOCKS listener, Mihomo outbound SOCKS node, and a private HTTP target.
Fixture DNS servers record every question; no operating-system DNS configuration is changed.

Failure cases to cover:

- Rules accidentally depend on row order; exact loses to wildcard; broad suffix beats narrow suffix.
- `*.example.test` fails for deep subdomains or incorrectly includes the apex; `+.example.test` misses apex.
- Duplicate patterns differing by case/trailing dot, malformed wildcard, empty upstream lists, unsafe schemes,
  proxy fragments, credentials, invalid ports, and recursive bootstrap are accepted.
- Provider hosts lose priority; hostname aliases skip node policy; irrelevant destination/download DNS is hijacked.
- A node excluded by the dialer-proxy filter incorrectly expands custom DNS scope; its hostname must still use system DNS.
- Matched NXDOMAIN or transport failure silently falls back to system DNS; unconfigured default stops system fallback.
- DNS update changes node server/SNI, projection credentials, or opens a DNS listener.
- Provider refresh drops DNS policy; invalid update or failed persistence publishes rejected policy.
- Restart loses settings; old schema or old API client clears existing DNS settings.
- UDP/TCP/DoH/DoT fail to use their actual transports; encrypted hostname resolution recurses.

Run at the end of implementation:

```sh
SURGEEB_NODE_DNS_E2E=1 SURGEEB_NODE_DNS_ARTIFACT=/tmp/surgeeb-node-dns-e2e.json \
  go test ./internal/management -run '^TestNodeDNSEndToEnd$' -count=1 -v
```

The JSON artifact contains scenario results, fixture query logs, redacted node DNS configuration,
and transport metadata. It contains no subscription URLs or gateway credentials.
