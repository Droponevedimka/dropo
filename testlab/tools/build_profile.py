#!/usr/bin/env python3
"""Merge all client DPI fingerprints into a single censor profile.

Reads:
  testlab/services.json          - SNI/CIDR catalog per service
  testlab/fingerprints/*.json    - client-submitted fingerprints (one per ISP)

Writes:
  testlab/profiles/censor-profile.json  - what censor/apply.sh enforces

Aggregation: across all fingerprints, each service takes its MOST AGGRESSIVE
observed verdict (ip-block > tls-rst > tls-drop > dns-poison). With no
fingerprints a sensible RU baseline is used so the lab works out of the box.
"""
import json
import os
import sys
import glob
import datetime

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SERVICES = os.path.join(ROOT, "services.json")
FP_DIR = os.path.join(ROOT, "fingerprints")
OUT = os.path.join(ROOT, "profiles", "censor-profile.json")

# higher = more aggressive; wins when merging across fingerprints
PRIORITY = {"ok": 0, "dns-poison": 1, "tls-drop": 2, "tls-rst": 3, "ip-block": 4, "unknown": 0}

# Used when fingerprints/ is empty — typical RU TSPU behaviour.
BASELINE = {
    "youtube": "tls-rst", "discord": "tls-rst", "twitter": "tls-rst",
    "linkedin": "tls-rst", "signal": "tls-rst", "snapchat": "tls-rst",
    "twitch": "tls-rst", "spotify": "tls-rst", "tiktok": "tls-rst",
    "facetime": "tls-rst", "viber": "tls-rst",
    "meta": "ip-block", "whatsapp": "ip-block", "telegram": "ip-block",
}
# Services that are also throttled (not just blocked).
THROTTLE = {"youtube"}


def load_json(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def main():
    catalog = load_json(SERVICES)["services"]
    verdicts = {}  # tag -> worst verdict
    sources = []

    fps = sorted(glob.glob(os.path.join(FP_DIR, "*.json")))
    fps = [p for p in fps if os.path.basename(p) != "schema.json"]
    for path in fps:
        try:
            fp = load_json(path)
        except Exception as e:
            print(f"[skip] {path}: {e}", file=sys.stderr)
            continue
        if fp.get("schema") != 1:
            print(f"[skip] {path}: unsupported schema", file=sys.stderr)
            continue
        sources.append(os.path.basename(path))
        for svc in fp.get("services", []):
            tag, v = svc.get("tag"), svc.get("verdict", "ok")
            if not tag or tag not in catalog:
                continue
            if PRIORITY.get(v, 0) > PRIORITY.get(verdicts.get(tag, "ok"), 0):
                verdicts[tag] = v

    if not verdicts:
        print("[info] no fingerprints found -> using RU baseline", file=sys.stderr)
        verdicts = dict(BASELINE)

    profile = {
        "generatedAt": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "sources": sources or ["<baseline>"],
        "tls_rst": [], "tls_drop": [], "ip_drop": [], "throttle": [],
    }
    for tag, verdict in sorted(verdicts.items()):
        cat = catalog.get(tag, {})
        sni = cat.get("sni", [])
        cidr = cat.get("ipcidr", [])
        if verdict == "ip-block":
            # Real TSPU blocks IP-blocked services (Meta/Telegram) by BOTH the IP
            # range AND the SNI. Emit both: the CIDRs catch raw-IP/QUIC flows, the
            # SNI-RST catches TLS regardless of which (CDN) IP DNS returns.
            profile["ip_drop"] += cidr
            profile["tls_rst"] += sni
        elif verdict == "tls-drop":
            profile["tls_drop"] += sni
        elif verdict in ("tls-rst", "dns-poison"):
            profile["tls_rst"] += sni
        if tag in THROTTLE:
            profile["throttle"] += sni

    for k in ("tls_rst", "tls_drop", "ip_drop", "throttle"):
        profile[k] = sorted(set(profile[k]))

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(profile, f, indent=2, ensure_ascii=False)
    print(f"[ok] wrote {OUT}")
    print(f"     sources={len(sources)} tls_rst={len(profile['tls_rst'])} "
          f"tls_drop={len(profile['tls_drop'])} ip_drop={len(profile['ip_drop'])} "
          f"throttle={len(profile['throttle'])}")


if __name__ == "__main__":
    main()
