#!/bin/sh
# Routes itself through the censor, then verifies the emulated blocking matches
# what the real ISP does: blocked SNIs get RST/timeout, a control host works,
# and an IP-blocked range times out.
set -u
CENSOR_IP="${CENSOR_IP:-172.31.66.2}"
echo "[tester] routing default via censor $CENSOR_IP"
ip route replace default via "$CENSOR_IP" || echo "[tester] WARN: could not set route"
sleep 2

pass=0; fail=0
# $1 = label, $2 = expect (block|ok), $3.. = curl args
check() {
  label="$1"; expect="$2"; shift 2
  out=$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 6 --max-time 12 "$@" 2>&1)
  rc=$?
  if [ "$expect" = "ok" ]; then
    if [ "$rc" = "0" ] && [ "$out" != "000" ]; then
      echo "  PASS  $label -> http $out"; pass=$((pass+1))
    else
      echo "  FAIL  $label -> rc=$rc code=$out (expected reachable)"; fail=$((fail+1))
    fi
  else  # expect block
    if [ "$rc" != "0" ] || [ "$out" = "000" ]; then
      echo "  PASS  $label -> blocked (rc=$rc)"; pass=$((pass+1))
    else
      echo "  FAIL  $label -> http $out (expected blocked)"; fail=$((fail+1))
    fi
  fi
}

echo "[tester] running checks..."
check "control example.com"   ok    https://example.com
check "youtube (SNI rst)"     block https://www.youtube.com
check "discord (SNI rst)"     block https://discord.com
check "telegram DC (ip drop)" block https://149.154.167.51 --resolve dummy:443:149.154.167.51
check "instagram (ip drop)"   block https://www.instagram.com

echo "[tester] DONE: $pass passed, $fail failed"
[ "$fail" = "0" ]
