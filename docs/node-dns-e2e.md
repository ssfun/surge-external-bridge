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
- Empty bootstrap rejects encrypted hostname upstreams instead of resolving only the upstream hostname through system DNS.
- An encrypted upstream hostname overlapping a node rule recurses into that rule.
- Explicit UDP/TCP bootstrap is ignored, or its NXDOMAIN/transport failures fall back to system DNS.
- Empty-bootstrap DoH/DoT node NXDOMAIN leaks the node hostname to system DNS.
- GET/PUT/restart loses an empty-bootstrap +.rfsconfig.com rule; use private fixtures, never contact that domain.
- UDP/TCP/DoH/DoT fail to use their actual transports; encrypted hostname resolution recurses.

Run at the end of implementation:

```sh
SURGEEB_NODE_DNS_E2E=1 SURGEEB_NODE_DNS_ARTIFACT=/tmp/surgeeb-node-dns-e2e.json \
  go test ./internal/management -run '^TestNodeDNSEndToEnd$' -count=1 -v
```

The JSON artifact contains scenario results, fixture query logs, redacted node DNS configuration,
and transport metadata. It contains no subscription URLs or gateway credentials.

## Node DNS diagnostic

Node cards → 更多 → 节点 DNS 诊断. The adjacent checkbox requests an isolated,
fresh application resolver; it does not flush the resolver serving live traffic.
The authenticated, same-origin endpoint is `POST /api/nodes/{id}/dns-diagnose`
with `{"bypass_cache":false}` (or `true`). It accepts only a current projected
node ID, never a caller-provided hostname; unknown fields are rejected.

The response captures the original node hostname, accepted Provider hosts alias
chain, effective domain, matching rule, configured resolver group and bootstrap
servers, IPv4/IPv6 addresses/errors/durations and a timestamp. A concurrent reload
cannot replace the captured snapshot. A deleted node returns 404; a node deleted
after capture can complete using that captured configuration. The configured node
diagnostic timeout and request cancellation also apply to DNS queries.

`cache_status: unknown` means Mihomo does not expose a reliable cache-hit flag.
`bypassed` means a fresh equivalent native resolver bypassed the application DNS
cache, not upstream caches or OS caches used for bootstrap. System DNS always
reports unknown and `bypass_applied: false`. Hosts and literal IPs report
`not_applicable`. Resolver lists identify the configured selected group, not the
upstream that won Mihomo's concurrent query. Native/system hosts can short-circuit
individual address families and are labeled in each family's `source`.

The same opt-in E2E above records diagnostic responses and DNS counters in its
JSON artifact, covering rules, aliases, hosts, literal IPs, explicit failures,
bypass, system-cache limitations, authentication, origin, input restriction and
missing/deleted nodes.
