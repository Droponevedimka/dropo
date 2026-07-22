#!/usr/bin/env python3
"""Stateful DPI censor — faithful TSPU-style model for the dropo lab.

Unlike the naive xt_string censor, this one REASSEMBLES the TCP stream before
reading the SNI, so plain TCP split/multisplit does NOT pass for free — exactly
like a real reassembling middlebox. It exposes the knobs the desync tricks
exploit, so each native Dropo strategy is exercised honestly:

  REASSEMBLE_POLICY = first|last   how overlapping bytes resolve  -> models seqovl
  VALIDATE_CHECKSUM = 0|1          ignore bad-checksum segments   -> models fooling=badsum
  MIN_TTL           = 0..255       ignore segments below this TTL -> models fake-ttl / fooling=datanoack
  QUIC              = 0|1          inspect+block QUIC Initial SNI  -> models fake-quic

Block actions: TLS -> spoofed RST to the client + drop; QUIC -> drop the Initial;
IP CIDRs from the profile -> dropped by iptables (handled in entrypoint).

Run `python3 stateful.py --selftest` to verify the engine without NFQUEUE.
"""
import json
import os
import sys
import struct

# ---------------------------------------------------------------- TLS parsing

def parse_client_hello_sni(buf):
    """(state, sni) where state in {complete, incomplete, notls}."""
    if len(buf) < 5:
        return ("incomplete", None)
    if buf[0] != 0x16:  # handshake record
        return ("notls", None)
    rec_len = int.from_bytes(buf[3:5], "big")
    if len(buf) < 5 + rec_len:
        return ("incomplete", None)
    hs = buf[5:5 + rec_len]
    if len(hs) < 4 or hs[0] != 0x01:  # ClientHello
        return ("notls", None)
    hlen = int.from_bytes(hs[1:4], "big")
    body = hs[4:4 + hlen]
    if len(body) < hlen:
        return ("incomplete", None)
    p = 2 + 32  # legacy_version + random
    if p + 1 > len(body):
        return ("incomplete", None)
    sid_len = body[p]; p += 1 + sid_len
    if p + 2 > len(body):
        return ("incomplete", None)
    cs_len = int.from_bytes(body[p:p + 2], "big"); p += 2 + cs_len
    if p + 1 > len(body):
        return ("incomplete", None)
    comp_len = body[p]; p += 1 + comp_len
    if p + 2 > len(body):
        return ("complete", None)  # no extensions
    ext_total = int.from_bytes(body[p:p + 2], "big"); p += 2
    ext_end = min(p + ext_total, len(body))
    while p + 4 <= ext_end:
        etype = int.from_bytes(body[p:p + 2], "big")
        elen = int.from_bytes(body[p + 2:p + 4], "big")
        p += 4
        edata = body[p:p + elen]; p += elen
        if etype == 0x0000:  # server_name
            if len(edata) < 5:
                return ("complete", None)
            q = 2
            while q + 3 <= len(edata):
                ntype = edata[q]
                nlen = int.from_bytes(edata[q + 1:q + 3], "big")
                q += 3
                name = edata[q:q + nlen]; q += nlen
                if ntype == 0:
                    return ("complete", name.decode("latin1").lower())
            return ("complete", None)
    return ("complete", None)


def sni_blocked(sni, blocklist):
    if not sni:
        return False
    return any(s in sni for s in blocklist)


# ------------------------------------------------------- TCP reassembly (flow)

class Flow:
    """Reassembles client->server bytes, modelling overlap resolution."""
    __slots__ = ("base", "buf", "verdict")

    def __init__(self):
        self.base = None       # initial sequence of first data byte
        self.buf = {}          # offset -> byte
        self.verdict = None    # None | "pass" | "block"

    def add(self, seq, data, policy):
        if not data:
            return
        if self.base is None:
            self.base = seq
        off = seq - self.base
        if off < 0:  # segment starts before current base (seqovl with lower seq)
            shift = -off
            self.buf = {k + shift: v for k, v in self.buf.items()}
            self.base = seq
            off = 0
        for i, b in enumerate(data):
            o = off + i
            if o in self.buf:
                if policy == "last":
                    self.buf[o] = b
                # policy == first: keep earliest byte
            else:
                self.buf[o] = b

    def contiguous(self):
        out = bytearray()
        i = 0
        while i in self.buf:
            out.append(self.buf[i])
            i += 1
        return bytes(out)


def decide_flow(flow, blocklist):
    """Return 'block' | 'pass' | 'wait' from the reassembled prefix."""
    if flow.verdict:
        return flow.verdict
    state, sni = parse_client_hello_sni(flow.contiguous())
    if state == "incomplete":
        return "wait"
    if state == "notls":
        flow.verdict = "pass"
        return "pass"
    flow.verdict = "block" if sni_blocked(sni, blocklist) else "pass"
    return flow.verdict


# --------------------------------------------------------------- QUIC (v1)

QUIC_SALT_V1 = bytes.fromhex("38762cf7f55934b34d179ae6a4c80cadccbb7f0a")


def _hkdf_expand_label(secret, label, length):
    from cryptography.hazmat.primitives.kdf.hkdf import HKDFExpand
    from cryptography.hazmat.primitives import hashes
    full = b"tls13 " + label
    info = struct.pack("!H", length) + bytes([len(full)]) + full + b"\x00"
    return HKDFExpand(algorithm=hashes.SHA256(), length=length, info=info).derive(secret)


def _quic_client_keys(dcid):
    import hmac
    import hashlib
    initial = hmac.new(QUIC_SALT_V1, dcid, hashlib.sha256).digest()  # HKDF-Extract
    csec = _hkdf_expand_label(initial, b"client in", 32)
    key = _hkdf_expand_label(csec, b"quic key", 16)
    iv = _hkdf_expand_label(csec, b"quic iv", 12)
    hp = _hkdf_expand_label(csec, b"quic hp", 16)
    return key, iv, hp


def _varint(buf, pos):
    b0 = buf[pos]
    ln = 1 << (b0 >> 6)
    val = b0 & 0x3F
    for i in range(1, ln):
        val = (val << 8) | buf[pos + i]
    return val, pos + ln


def quic_initial_sni(packet):
    """Extract SNI from a QUIC v1 Initial packet, or None."""
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
        if len(packet) < 7 or not (packet[0] & 0x80):
            return None
        if packet[1:5] != b"\x00\x00\x00\x01":
            return None
        pos = 5
        dcid_len = packet[pos]; pos += 1
        dcid = packet[pos:pos + dcid_len]; pos += dcid_len
        scid_len = packet[pos]; pos += 1
        pos += scid_len
        tok_len, pos = _varint(packet, pos)
        pos += tok_len
        _length, pos = _varint(packet, pos)
        pn_offset = pos

        key, iv, hp = _quic_client_keys(dcid)
        sample = packet[pn_offset + 4:pn_offset + 20]
        enc = Cipher(algorithms.AES(hp), modes.ECB()).encryptor()
        mask = enc.update(sample) + enc.finalize()
        first = packet[0] ^ (mask[0] & 0x0F)
        pn_len = (first & 0x03) + 1
        pn_bytes = bytes(packet[pn_offset + i] ^ mask[1 + i] for i in range(pn_len))
        pn = int.from_bytes(pn_bytes, "big")

        header = bytearray(packet[:pn_offset + pn_len])
        header[0] = first
        for i in range(pn_len):
            header[pn_offset + i] = pn_bytes[i]
        payload = packet[pn_offset + pn_len:]

        nonce = bytes(iv[i] ^ (pn.to_bytes(12, "big")[i]) for i in range(12))
        from cryptography.hazmat.primitives.ciphers.aead import AESGCM
        plain = AESGCM(key).decrypt(nonce, bytes(payload), bytes(header))

        # parse frames -> reassemble CRYPTO -> TLS ClientHello
        crypto = {}
        i = 0
        while i < len(plain):
            ft = plain[i]; i += 1
            if ft == 0x00:  # PADDING
                continue
            if ft == 0x01:  # PING
                continue
            if ft == 0x06:  # CRYPTO
                off, i = _varint(plain, i)
                ln, i = _varint(plain, i)
                for j in range(ln):
                    crypto[off + j] = plain[i + j]
                i += ln
                continue
            break  # unknown frame; stop
        data = bytearray()
        k = 0
        while k in crypto:
            data.append(crypto[k]); k += 1
        _state, sni = parse_client_hello_sni(bytes(data))
        return sni
    except Exception:
        return None


# ------------------------------------------------------------- NFQUEUE runtime

def run_nfqueue(profile):
    from netfilterqueue import NetfilterQueue
    from scapy.all import IP, TCP, UDP, send

    policy = os.environ.get("REASSEMBLE_POLICY", "last")
    validate_cksum = os.environ.get("VALIDATE_CHECKSUM", "0") == "1"
    min_ttl = int(os.environ.get("MIN_TTL", "0"))
    quic_on = os.environ.get("QUIC", "1") == "1"
    block = [s.lower() for s in profile.get("tls_rst", []) + profile.get("tls_drop", [])]
    drop_set = set(s.lower() for s in profile.get("tls_drop", []))

    flows = {}
    print(f"[stateful] policy={policy} checksum={validate_cksum} min_ttl={min_ttl} "
          f"quic={quic_on} sni_rules={len(block)}", flush=True)

    def good_checksum(pkt):
        if not validate_cksum:
            return True
        raw = bytes(pkt)
        recomputed = bytes(IP(raw))  # scapy recomputes on rebuild
        return raw == recomputed

    def cb(qpkt):
        ip = IP(qpkt.get_payload())
        # QUIC: UDP/443 Initial
        if quic_on and ip.haslayer(UDP) and ip[UDP].dport == 443:
            sni = quic_initial_sni(bytes(ip[UDP].payload))
            if sni_blocked(sni, block):
                qpkt.drop()
                return
            qpkt.accept()
            return
        if not ip.haslayer(TCP) or ip[TCP].dport not in (443, 80):
            qpkt.accept(); return
        t = ip[TCP]
        payload = bytes(t.payload)
        if min_ttl and ip.ttl < min_ttl:
            qpkt.accept(); return       # censor ignores low-TTL fake; server drops it anyway
        if not good_checksum(ip):
            qpkt.accept(); return       # bad checksum: ignored for inspection (fooled if not validating)
        key = (ip.src, t.sport, ip.dst, t.dport)
        fl = flows.get(key)
        if fl is None:
            fl = flows[key] = Flow()
        if payload:
            fl.add(t.seq, payload, policy)
        verdict = decide_flow(fl, block)
        if verdict == "block":
            # spoof a RST from the server back to the client (passive DPI)
            rst = IP(src=ip.dst, dst=ip.src) / TCP(sport=t.dport, dport=t.sport,
                    flags="R", seq=t.ack)
            send(rst, verbose=0)
            qpkt.drop()
            return
        qpkt.accept()

    nfq = NetfilterQueue()
    nfq.bind(int(os.environ.get("QUEUE_NUM", "0")), cb)
    try:
        nfq.run()
    finally:
        nfq.unbind()


# ----------------------------------------------------------------- self-test

def _build_client_hello(sni):
    sni_b = sni.encode()
    server_name = b"\x00" + struct.pack("!H", len(sni_b)) + sni_b
    sn_list = struct.pack("!H", len(server_name)) + server_name
    ext = b"\x00\x00" + struct.pack("!H", len(sn_list)) + sn_list
    exts = struct.pack("!H", len(ext)) + ext
    body = b"\x03\x03" + b"\x00" * 32 + b"\x00" + b"\x00\x02\x13\x01" + b"\x01\x00" + exts
    hs = b"\x01" + struct.pack("!I", len(body))[1:] + body
    rec = b"\x16\x03\x01" + struct.pack("!H", len(hs)) + hs
    return rec


def _selftest():
    block = ["youtube.com", "discord.com"]
    ok = 0
    fail = 0

    def check(name, cond):
        nonlocal ok, fail
        if cond:
            ok += 1; print(f"  PASS  {name}")
        else:
            fail += 1; print(f"  FAIL  {name}")

    # 1. plain blocked / allowed
    ch = _build_client_hello("www.youtube.com")
    st, sni = parse_client_hello_sni(ch)
    check("parse plain SNI", st == "complete" and sni == "www.youtube.com")
    check("plain youtube blocked", sni_blocked(sni, block))
    _, sni2 = parse_client_hello_sni(_build_client_hello("example.com"))
    check("plain example allowed", not sni_blocked(sni2, block))

    # 2. TCP split: SNI spans a segment boundary -> reassembled & blocked
    cut = ch.find(b"youtube") + 3
    f = Flow()
    f.add(1000, ch[:cut], "last")
    check("split incomplete before 2nd seg", decide_flow(f, block) == "wait")
    f.add(1000 + cut, ch[cut:], "last")
    check("TCP split still BLOCKED (reassembled)", decide_flow(f, block) == "block")

    # 3. multisplit (3 segments across the SNI) -> blocked
    f = Flow()
    a, b = ch.find(b"you"), ch.find(b"tube") + 2
    f.add(5000, ch[:a], "last"); f.add(5000 + a, ch[a:b], "last"); f.add(5000 + b, ch[b:], "last")
    check("multisplit BLOCKED (reassembled)", decide_flow(f, block) == "block")

    # 4. seqovl: a fake decoy ClientHello sent first, then the real one overlapping.
    #    policy=first -> decoy bytes win -> SNI looks allowed -> PASS (bypass modelled)
    #    policy=last  -> real bytes win  -> BLOCK
    decoy = _build_client_hello("example.com")
    real = _build_client_hello("www.youtube.com")
    n = min(len(decoy), len(real))
    ff = Flow(); ff.add(2000, decoy[:n], "first"); ff.add(2000, real[:n], "first"); ff.add(2000 + n, real[n:], "first")
    check("seqovl bypass works under policy=first", decide_flow(ff, block) == "pass")
    fl = Flow(); fl.add(2000, decoy[:n], "last"); fl.add(2000, real[:n], "last"); fl.add(2000 + n, real[n:], "last")
    check("seqovl blocked under policy=last", decide_flow(fl, block) == "block")

    # 5. QUIC round-trip (requires cryptography)
    try:
        pkt = _make_quic_initial("www.youtube.com")
        sni = quic_initial_sni(pkt)
        check("QUIC Initial SNI extracted", sni == "www.youtube.com")
    except Exception as e:
        print(f"  SKIP  QUIC selftest ({e})")

    print(f"[selftest] {ok} passed, {fail} failed")
    return fail == 0


def _make_quic_initial(sni):
    """Encrypt a minimal QUIC v1 Initial carrying a ClientHello (selftest only)."""
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    dcid = bytes.fromhex("8394c8f03e515708")
    key, iv, hp = _quic_client_keys(dcid)
    ch = _build_client_hello(sni)
    crypto = b"\x06" + _enc_varint(0) + _enc_varint(len(ch)) + ch  # CRYPTO frame
    payload = crypto + b"\x00" * (1200 - len(crypto))  # PADDING to min size
    pn = 0
    pn_bytes = b"\x00"
    first = 0xC0 | (len(pn_bytes) - 1)
    length = len(pn_bytes) + len(payload) + 16
    hdr = bytes([first]) + b"\x00\x00\x00\x01" + bytes([len(dcid)]) + dcid + b"\x00" + _enc_varint(0) + _enc_varint(length)
    pn_off = len(hdr)
    aad = hdr + pn_bytes
    nonce = bytes(iv[i] ^ pn.to_bytes(12, "big")[i] for i in range(12))
    ct = AESGCM(key).encrypt(nonce, payload, aad)
    sample = ct[4 - len(pn_bytes):4 - len(pn_bytes) + 16]
    enc = Cipher(algorithms.AES(hp), modes.ECB()).encryptor()
    mask = enc.update(sample) + enc.finalize()
    first ^= mask[0] & 0x0F
    pn_bytes = bytes(pn_bytes[i] ^ mask[1 + i] for i in range(len(pn_bytes)))
    return bytes([first]) + hdr[1:] + pn_bytes + ct


def _enc_varint(v):
    if v < 64:
        return bytes([v])
    if v < 16384:
        return struct.pack("!H", v | 0x4000)
    return struct.pack("!I", v | 0x80000000)


def main():
    if "--selftest" in sys.argv:
        sys.exit(0 if _selftest() else 1)
    profile_path = os.environ.get("PROFILE", "/profile/censor-profile.json")
    with open(profile_path, "r", encoding="utf-8") as f:
        profile = json.load(f)
    run_nfqueue(profile)


if __name__ == "__main__":
    main()
