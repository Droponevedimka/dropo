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
- Work-network/WireGuard overlay rules have priority over service strategies.
  Do not allow private destinations to fall through to a public VPN source.
- A strategy selector must validate every required TCP, UDP and web target
  before committing. Never persist a partial-success candidate.
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
