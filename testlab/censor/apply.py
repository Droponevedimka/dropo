#!/usr/bin/env python3
"""Emulated DPI censor. Applies the generated profile as netfilter rules on the
FORWARD path so a client routed through this box experiences the same blocking
as the real ISP:

  tls_rst[]  SNI substring in ClientHello -> REJECT with tcp-reset  (passive DPI)
  tls_drop[] SNI substring                -> DROP                   (silent drop)
  ip_drop[]  destination CIDR             -> DROP                   (IP block: TG/Meta)
  throttle[] SNI substring                -> MARK -> tc htb 256kbit (shaping)

Uses iptables xt_string match (no NFQUEUE / C build). Requires NET_ADMIN and an
xt_string-capable host kernel.
"""
import json
import os
import subprocess
import sys

PROFILE = os.environ.get("PROFILE", "/profile/censor-profile.json")
THROTTLE_RATE = os.environ.get("THROTTLE_RATE", "256kbit")
IFACE = os.environ.get("EXT_IFACE", "eth0")


def run(cmd, check=True):
    print("+", " ".join(cmd))
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.stdout.strip():
        print(r.stdout.strip())
    if r.returncode != 0:
        print(f"  ! {r.stderr.strip()}", file=sys.stderr)
        if check:
            raise SystemExit(r.returncode)
    return r


def sni_rule(target_args, sni):
    # match the SNI substring on TLS(443) and HTTP Host(80)
    for dport in ("443", "80"):
        run(["iptables", "-A", "FORWARD", "-p", "tcp", "--dport", dport,
             "-m", "string", "--algo", "bm", "--string", sni] + target_args, check=False)


def main():
    with open(PROFILE, "r", encoding="utf-8") as f:
        prof = json.load(f)
    print(f"[censor] profile generated={prof.get('generatedAt')} sources={prof.get('sources')}")

    run(["sysctl", "-w", "net.ipv4.ip_forward=1"], check=False)
    run(["iptables", "-F", "FORWARD"], check=False)
    run(["iptables", "-t", "mangle", "-F", "FORWARD"], check=False)
    run(["iptables", "-t", "nat", "-F", "POSTROUTING"], check=False)
    run(["iptables", "-t", "nat", "-A", "POSTROUTING", "-o", IFACE, "-j", "MASQUERADE"], check=False)

    # throttle: htb root with a slow class for fwmark 7
    run(["tc", "qdisc", "del", "dev", IFACE, "root"], check=False)
    if prof.get("throttle"):
        run(["tc", "qdisc", "add", "dev", IFACE, "root", "handle", "1:", "htb", "default", "10"], check=False)
        run(["tc", "class", "add", "dev", IFACE, "parent", "1:", "classid", "1:10", "htb", "rate", "1000mbit"], check=False)
        run(["tc", "class", "add", "dev", IFACE, "parent", "1:", "classid", "1:7", "htb", "rate", THROTTLE_RATE, "ceil", THROTTLE_RATE], check=False)
        run(["tc", "filter", "add", "dev", IFACE, "parent", "1:", "protocol", "ip", "handle", "7", "fw", "flowid", "1:7"], check=False)
        for sni in prof["throttle"]:
            run(["iptables", "-t", "mangle", "-A", "FORWARD", "-p", "tcp", "--dport", "443",
                 "-m", "string", "--algo", "bm", "--string", sni, "-j", "MARK", "--set-mark", "7"], check=False)

    for cidr in prof.get("ip_drop", []):
        run(["iptables", "-A", "FORWARD", "-d", cidr, "-j", "DROP"], check=False)
    for sni in prof.get("tls_drop", []):
        sni_rule(["-j", "DROP"], sni)
    for sni in prof.get("tls_rst", []):
        sni_rule(["-j", "REJECT", "--reject-with", "tcp-reset"], sni)

    n = len(prof.get("tls_rst", [])) + len(prof.get("tls_drop", [])) + len(prof.get("ip_drop", []))
    print(f"[censor] active: {n} block rules, {len(prof.get('throttle', []))} throttle rules. Forwarding for clients.")
    # keep running
    subprocess.run(["tail", "-f", "/dev/null"])


if __name__ == "__main__":
    main()
