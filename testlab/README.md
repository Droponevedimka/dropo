# dropo DPI censor lab

> **zapret runtime:** dropo 2.1.55 and newer test only
> [bol-van/zapret2](https://github.com/bol-van/zapret2) through `winws2.exe` and
> its Lua strategy API. The old zapret1 `winws.exe`/`--dpi-desync` runtime is no
> longer packaged, launched, or supported by the Windows client.

Reproduce RU/TSPU-style blocking **outside Russia** so DPI-bypass work can be
developed and regression-tested without shipping builds to end users.

The lab runs a local **censor** (a Linux gateway that emulates the ISP) driven by
real **fingerprints** captured from clients. Route a test machine — ideally a
Windows VM running dropo+winws2 — through the censor and you see the same RST /
silent-drop / IP-block / throttle behaviour as the real network.

```
[ Windows VM: dropo + winws2 ]  →  [ Linux censor (this lab) ]  →  internet
```

## How blocking is emulated

The censor (`censor/apply.py`) turns the merged profile into netfilter rules on
the FORWARD path — the classic passive-DPI emulation, using iptables `xt_string`
(no NFQUEUE / kernel build):

| Profile field | Real-world analog | Rule |
|---|---|---|
| `tls_rst[]`  | TSPU reads SNI, injects RST | `--string <sni> -j REJECT --reject-with tcp-reset` |
| `tls_drop[]` | silent drop by SNI          | `--string <sni> -j DROP` |
| `ip_drop[]`  | IP block (Telegram, Meta)   | `-d <cidr> -j DROP` |
| `throttle[]` | speed shaping (YouTube)     | `-j MARK` + `tc htb` |

## Quick start (self-test, no VM needed)

```bash
cd testlab
python tools/build_profile.py            # fingerprints + services.json -> profiles/censor-profile.json
docker compose up -d --build censor      # censor becomes gateway 172.31.66.2
docker compose run --rm tester           # routes through censor, asserts blocking
docker compose down
```

Expected: control host passes, YouTube/Discord get RST, Telegram/Instagram time
out. (Verified: 5/5 — needs an `xt_string`-capable host kernel; Docker
Desktop/WSL2 has it.)

## Full-coverage (stateful) censor

The naive censor above is fooled by anything that splits the SNI — so a
split-only winws2 strategy would *pass in the lab but fail in RF*. The **stateful**
censor (`censor/stateful.py`, NFQUEUE) closes that gap: it **reassembles the TCP
stream before reading the SNI**, exactly like a real reassembling TSPU, and
exposes the knobs each desync trick targets.

```bash
docker compose -f docker-compose.yml -f docker-compose.stateful.yml up -d --build censor
docker compose -f docker-compose.yml -f docker-compose.stateful.yml run --rm tester
# engine unit-tests (no kernel needed):
docker run --rm --entrypoint python3 testlab-censor-stateful /stateful.py --selftest
```

What each knob models (set in `docker-compose.stateful.yml`):

| winws2 / zapret2 technique | knob | effect |
|---|---|---|
| `split` / `multisplit` (TCP split) | — | **always blocked** (stream is reassembled) — no free pass |
| `--lua-desync=multisplit:seqovl=…` (overlap) | `REASSEMBLE_POLICY=first\|last` | bypass works only if the policy takes the fake overlapping bytes |
| `fooling=badsum` (fake bad checksum) | `VALIDATE_CHECKSUM=0\|1` | `0` → fake is inspected (badsum fools it); `1` → fake ignored (badsum defeated) |
| `fake-ttl` / low-TTL fake | `MIN_TTL=0..255` | `0` → fake inspected (ttl trick works); high → fake ignored |
| `fake-quic` (QUIC desync) | `QUIC=1` | QUIC Initial is decrypted and matched on SNI |

Verified: engine **9/9** (split & multisplit stay blocked; seqovl bypasses under
`policy=first`, blocked under `policy=last`; QUIC SNI decrypted) and NFQUEUE
runtime **5/5**. Needs `nfnetlink_queue` in the host kernel — Docker Desktop/WSL2
(kernel 6.6) has it (confirmed).

To check whether a strategy beats a *given* ISP, set the knobs to that ISP's
fingerprint (e.g. a TSPU that validates checksums → `VALIDATE_CHECKSUM=1`) and
rerun. A strategy that passes the stateful censor with the RF knobs is real
evidence it will work in RF — not just that it split the SNI.

## Validate the real dropo app

1. `docker compose up -d censor`, note its IP (`172.31.66.2`).
2. Put a Windows VM on the same Docker/host network and set its default gateway
   to the censor (or use a Linux bridge so the VM routes through it).
3. Run dropo there. winws2/WinDivert rewrites packets on the VM NIC; the censor
   sees the rewritten packets and reacts exactly like the real DPI — so a winws2
   strategy that defeats the censor will defeat the real ISP with the same
   fingerprint.

Tight loop tip: you don't need to rebuild dropo to iterate strategies — copy the
`winws2.exe … --new …` command from the client log
(`[Zapret2] starting Per-service bypass: …`) and run it on the VM behind the
censor. Rebuild dropo only once the strategy is proven.

## Updating the fingerprint database (from client reports)

Clients press **Настройки → Снять отпечаток** in dropo
(`App.CaptureDPIFingerprint`) and send you the JSON. To add it:

```bash
python tools/add_fingerprint.py path/to/dpi-fingerprint-20260627-101500.json --isp mts --country RU
```

This copies it into `fingerprints/` under a stable `<country>-<isp>-<ts>.json`
name and rebuilds the profile. The censor picks up the new profile on its next
start (`docker compose up -d --build censor`). Aggregation keeps the **most
aggressive** observed verdict per service across all fingerprints.

With no fingerprints yet, `build_profile.py` falls back to a sensible RU baseline
so the lab works out of the box.

## Layout

```
testlab/
  services.json              SNI substrings + IP CIDRs per service (keep in sync with app)
  fingerprints/              client-submitted fingerprints (the database) + schema.json
  profiles/censor-profile.json   generated; what the censor enforces
  tools/build_profile.py     merge fingerprints -> profile
  tools/add_fingerprint.py   add a client fingerprint + rebuild
  censor/                    Dockerfile + apply.py (iptables/tc censor)
  tester/                    Dockerfile + run-tests.sh (self-check)
  docker-compose.yml
```

## References

- [bol-van/zapret2](https://github.com/bol-van/zapret2) — winws2/nfqws2 Lua strategies and `blockcheck2`
- [Runnin4ik/dpi-detector](https://github.com/Runnin4ik/dpi-detector), [MayersScott/rkn-block-checker](https://github.com/MayersScott/rkn-block-checker) — fingerprint taxonomy (RST/DROP/SYN/TLS)
- [Kkevsterrr/geneva](https://github.com/Kkevsterrr/geneva) — censor-vs-evader testbed methodology
