# Repository instructions for AI agents

AI tools may modify and test the code, but they must never add themselves or
their vendors, models, sessions, bots, or service accounts to Git metadata.

- Keep the commit author and committer exactly as
  `Droponevedimka <34841931+Droponevedimka@users.noreply.github.com>`.
- Never add attribution trailers or markers such as `Co-Authored-By`,
  `Generated-By`, `Assisted-By`, `Signed-off-by`, or session URLs.
- Never change `git user.name`, `git user.email`, author, or committer values.
- Never bypass repository hooks with `--no-verify`.
- Before committing or pushing, run
  `pwsh -File tools/check-clean-contributors.ps1`.
- If an AI client cannot disable automatic attribution, leave the changes
  uncommitted for the repository owner.

These rules apply to every agent, subagent, editor assistant, and automated
commit workflow.

## Windows traffic architecture

- Windows release uses the in-process `app/trafficorchestrator` package as the
  only Dropo-owned WinDivert owner. Keep one handle and apply complete immutable
  `TrafficPlan` revisions atomically.
- Do not add an external anti-DPI executable, command-line strategy composer,
  Lua strategy runtime, Cygwin DLL, downloaded code, or shell execution to the
  Windows traffic path.
- New packet actions must be typed, bounded, fail-safe and covered by parser,
  fixture and plan-validation tests. Unknown or weakly classified traffic must
  pass unchanged.
- Service-specific or shared-provider IP sources must never participate in a
  global blocked-traffic catch-all. Scope them to verified service process,
  domain, or media evidence. In blocked-only mode, a known domain not present
  in blocked domain catalogs must take direct precedence over generic IP lists.
- Every `ServiceRule` containing `IPCIDRs` must declare an `IPMatchPolicy`.
  Named services and dynamic endpoints use `require_context`; only the generic
  signed blocked-IP catalog may use `hostless_only`. A sing-box service CIDR
  rule must include a process/package identity, never an IP match by itself.
- Preserve the shared-address regression: the same destination IP with a
  blocked service SNI may be transformed, while an unrelated SNI must be passed
  byte-for-byte unchanged. Keep the Windows packet emulation and Android route
  ordering tests in the release gate whenever classification changes.
- Work-network/WireGuard overlay rules have priority over service strategies.
  Do not allow private destinations to fall through to a public VPN source.
- A strategy selector must validate every required TCP, UDP and web target
  before committing. Never persist a partial-success candidate.
- The Windows connection-ready gate ends once sing-box/TUN, an immutable
  bootstrap `TrafficPlan`, and safe outbound selectors are active. Never wait
  synchronously in `Start()` for service probes, Discord media observation, or
  a complete strategy ladder; all automatic selection runs after the client is
  connected.
- At bootstrap, automatic blocked services may use a network-valid proven
  direct-strategy cache. Otherwise route them through the available VPN
  subscription fallback, or direct only when no subscription exists. Background
  selection must commit only after every required target succeeds. With a VPN
  subscription it tries one small bounded candidate ladder and falls back to the
  subscription immediately. Without a subscription it may repeat that bounded
  ladder in a cancellable campaign of at most one hour; the UI must expose the
  active service, strategy/attempt number, and the extended-search warning.
  Retry temporary VPN/direct fallbacks on each new session.
- Discord realtime/media health is background maintenance and must never set a
  connection-wide busy state or disable settings. Mark Discord working only
  after sustained bidirectional media evidence. Explicit user Direct/VPN policy
  remains authoritative and bypasses automatic discovery.
- Tie every background strategy task to the active VPN-session generation and
  abort it on Stop or process exit. A stale task must never restart or mutate the
  traffic engine after disconnect.
- VPN fallback is ordered between independent `VPNSource` entries. Do not turn
  sibling nodes inside one subscription into automatic fallback levels; the
  provider's first supported node or the user's manual choice is authoritative.
- Current Windows artifacts are self-contained. Do not reintroduce first-run
  downloads of executable dependencies or separate dependency/bypass assets.
  Runtime files must be covered by the signed core's file-level manifest and
  copied only into the ACL-protected ProgramData runtime.
- External packet-filter projects may be referenced only in
  `THIRD_PARTY_NOTICES.md` as research or license sources. Substantial source
  reuse requires an explicit license review, retained notices and dedicated
  tests before it enters production.
