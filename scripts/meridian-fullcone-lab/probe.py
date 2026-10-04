"""Strict evidence collection for the isolated lab, not a provider NAT label."""
import json
import os
import socket
import struct
import sys

COOKIE = 0x2112A442
PRIMARY = ("192.168.240.4", 3478)
ALTERNATE = ("192.168.240.5", 3479)
CLIENT = ("192.168.240.3", 1080)


class EvidenceError(Exception):
    pass


def require(condition, message):
    # Deliberately not assert: validation must survive Python -O.
    if not condition:
        raise EvidenceError(message)


def read_exact(stream, size):
    data = bytearray()
    while len(data) < size:
        part = stream.recv(size - len(data))
        require(bool(part), "truncated SOCKS handshake")
        data.extend(part)
    return bytes(data)


def associate(control):
    control.sendall(b"\x05\x01\x00")
    require(read_exact(control, 2) == b"\x05\x00", "SOCKS authentication rejected")
    control.sendall(b"\x05\x03\x00\x01" + bytes(6))
    require(read_exact(control, 4) == b"\x05\x00\x00\x01", "SOCKS UDP association rejected")
    response = read_exact(control, 6)
    relay = (socket.inet_ntoa(response[:4]), struct.unpack("!H", response[4:])[0])
    require(relay[0] == CLIENT[0] and relay[1] > 0, "SOCKS relay is outside the isolated client")
    return relay


def parse_response(received, txid, expected):
    require(len(received) >= 30, "truncated SOCKS/STUN response")
    require(received[:4] == b"\x00\x00\x00\x01", "unsupported SOCKS address or fragmentation")
    sender = (socket.inet_ntoa(received[4:8]), struct.unpack("!H", received[8:10])[0])
    require(sender == expected, "STUN response came from the wrong endpoint")
    data = received[10:]
    kind, length, cookie = struct.unpack("!HHI", data[:8])
    require(kind == 0x101 and cookie == COOKIE, "invalid STUN success header")
    require(length % 4 == 0 and length == len(data) - 20, "invalid STUN message length")
    require(data[8:20] == txid, "STUN transaction mismatch")
    mapped = None
    offset = 20
    while offset < len(data):
        require(offset + 4 <= len(data), "truncated STUN attribute")
        kind, length = struct.unpack("!HH", data[offset:offset + 4])
        end = offset + 4 + length
        padded_end = offset + 4 + (length + 3) // 4 * 4
        require(padded_end <= len(data), "truncated STUN attribute value")
        value = data[offset + 4:end]
        if kind == 0x20:
            require(mapped is None, "duplicate XOR-MAPPED-ADDRESS")
            require(length == 8 and value[:2] == b"\x00\x01", "invalid IPv4 XOR-MAPPED-ADDRESS")
            ip = socket.inet_ntoa(struct.pack("!I", struct.unpack("!I", value[4:])[0] ^ COOKIE))
            port = struct.unpack("!H", value[2:4])[0] ^ (COOKIE >> 16)
            require(ip != "0.0.0.0" and port > 0, "empty mapped endpoint")
            mapped = (ip, port)
        offset = padded_end
    require(mapped is not None, "missing XOR-MAPPED-ADDRESS")
    return {"reply": sender, "mapped": mapped}


def query(sock, relay, target, change, expected):
    txid = os.urandom(12)
    attrs = struct.pack("!HHI", 3, 4, change) if change else b""
    message = struct.pack("!HHI", 1, len(attrs), COOKIE) + txid + attrs
    header = b"\x00\x00\x00\x01" + socket.inet_aton(target[0]) + struct.pack("!H", target[1])
    sock.sendto(header + message, relay)
    received, source = sock.recvfrom(2048)
    require(source == relay, "UDP response did not come from the owned SOCKS relay")
    return {"change": change, **parse_response(received, txid, expected)}


def collect(sock, relay):
    results = [
        query(sock, relay, PRIMARY, 0, PRIMARY),
        query(sock, relay, PRIMARY, 6, ALTERNATE),
        query(sock, relay, PRIMARY, 2, (PRIMARY[0], ALTERNATE[1])),
        query(sock, relay, ALTERNATE, 0, ALTERNATE),
    ]
    require(len({tuple(result["mapped"]) for result in results}) == 1, "destination-dependent mapping")
    return results


def main():
    protocol = sys.argv[1] if len(sys.argv) == 2 else ""
    require(protocol in ("vless", "hy2"), "specify vless or hy2")
    try:
        with socket.create_connection(CLIENT, 3) as control:
            relay = associate(control)
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
                sock.bind(("0.0.0.0", 0))
                sock.settimeout(6)
                results = collect(sock, relay)
    except (EvidenceError, OSError) as error:
        # A failed probe does not establish a particular restrictive NAT type.
        reason = str(error) if isinstance(error, EvidenceError) else type(error).__name__
        print(json.dumps({"protocol": protocol, "scope": "isolated_lab", "status": "unconfirmed", "reason": reason}))
        return 1
    print(json.dumps({"protocol": protocol, "scope": "isolated_lab", "status": "passed", "ordinary": True,
                      "alternate_ip_port": True, "alternate_port": True,
                      "endpoint_independent_mapping": True, "evidence": results}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
