#!/bin/sh
# Stateful censor: IP CIDR drops + MASQUERADE via iptables, TCP/UDP 443 (+80)
# diverted to NFQUEUE where stateful.py reassembles and decides.
set -e
IFACE="${EXT_IFACE:-eth0}"
QNUM="${QUEUE_NUM:-0}"
PROFILE="${PROFILE:-/profile/censor-profile.json}"

sysctl -w net.ipv4.ip_forward=1 2>/dev/null || true
iptables -F FORWARD 2>/dev/null || true
iptables -t nat -F POSTROUTING 2>/dev/null || true
iptables -t nat -A POSTROUTING -o "$IFACE" -j MASQUERADE

# IP-block CIDRs straight to DROP (Telegram/Meta)
python3 - "$PROFILE" <<'PY'
import json,sys,subprocess
prof=json.load(open(sys.argv[1]))
for cidr in prof.get("ip_drop",[]):
    subprocess.run(["iptables","-A","FORWARD","-d",cidr,"-j","DROP"])
print(f"[entrypoint] ip_drop rules: {len(prof.get('ip_drop',[]))}")
PY

# divert the inspected traffic to the stateful engine
iptables -A FORWARD -p tcp --dport 443 -j NFQUEUE --queue-num "$QNUM"
iptables -A FORWARD -p tcp --dport 80  -j NFQUEUE --queue-num "$QNUM"
iptables -A FORWARD -p udp --dport 443 -j NFQUEUE --queue-num "$QNUM"

echo "[entrypoint] starting stateful censor (queue $QNUM)"
exec python3 /stateful.py
