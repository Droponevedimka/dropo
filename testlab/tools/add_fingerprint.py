#!/usr/bin/env python3
"""Add a client-submitted DPI fingerprint to the database and rebuild the profile.

Usage:
    python tools/add_fingerprint.py path/to/dpi-fingerprint-XXXX.json [--isp mts] [--country RU]

Copies the file into fingerprints/ under a stable, descriptive name, then runs
build_profile.py so the censor immediately reflects the new data.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import hashlib

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FP_DIR = os.path.join(ROOT, "fingerprints")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("file", help="client fingerprint JSON")
    ap.add_argument("--isp", default="", help="ISP label, e.g. mts/beeline")
    ap.add_argument("--country", default="", help="override country code")
    args = ap.parse_args()

    if not os.path.isfile(args.file):
        sys.exit(f"not found: {args.file}")
    with open(args.file, "r", encoding="utf-8") as f:
        fp = json.load(f)
    if fp.get("schema") != 1 or "services" not in fp:
        sys.exit("not a valid dropo fingerprint (schema 1 expected)")

    country = (args.country or fp.get("country") or "xx").lower()
    isp = (args.isp or "isp").lower().replace(" ", "-")
    captured = (fp.get("capturedAt") or "").replace(":", "").replace("-", "")[:13]
    digest = hashlib.sha1(json.dumps(fp, sort_keys=True).encode()).hexdigest()[:6]
    name = f"{country}-{isp}-{captured or digest}.json"

    os.makedirs(FP_DIR, exist_ok=True)
    dest = os.path.join(FP_DIR, name)
    shutil.copyfile(args.file, dest)
    print(f"[ok] added {dest}")

    blocked = [s["tag"] for s in fp["services"] if s.get("verdict", "ok") != "ok"]
    print(f"     blocked: {', '.join(blocked) or '(none)'}")

    subprocess.run([sys.executable, os.path.join(ROOT, "tools", "build_profile.py")], check=True)


if __name__ == "__main__":
    main()
