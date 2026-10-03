"""Make an actual HTTP request through SOCKS -> REALITY -> selected egress."""
import json
import secrets
import socket
import struct
import sys


def receive(sock, size):
    data = b""
    while len(data) < size:
        part = sock.recv(size - len(data))
        if not part:
            raise RuntimeError("truncated SOCKS response")
        data += part
    return data


def probe(expected):
    nonce = secrets.token_hex(16)
    with socket.create_connection(("192.168.241.3", 1080), 3) as sock:
        sock.settimeout(8)
        sock.sendall(b"\x05\x01\x00")
        if receive(sock, 2) != b"\x05\x00":
            raise RuntimeError("SOCKS negotiation failed")
        sock.sendall(b"\x05\x01\x00\x01" + socket.inet_aton("192.168.241.4") + struct.pack("!H", 8080))
        header = receive(sock, 4)
        if header[:3] != b"\x05\x00\x00":
            raise RuntimeError("SOCKS connection rejected")
        if header[3] == 1:
            receive(sock, 6)
        elif header[3] == 4:
            receive(sock, 18)
        elif header[3] == 3:
            receive(sock, receive(sock, 1)[0] + 2)
        else:
            raise RuntimeError("invalid SOCKS address")
        sock.sendall(f"GET /{nonce} HTTP/1.0\r\nHost: 192.168.241.4\r\n\r\n".encode())
        reply = b""
        while len(reply) <= 8192:
            part = sock.recv(4096)
            if not part:
                break
            reply += part
        if len(reply) > 8192:
            raise RuntimeError("oversized witness response")
        head, body = reply.split(b"\r\n\r\n", 1)
        if b" 200 " not in head.split(b"\r\n", 1)[0]:
            raise RuntimeError("witness request failed")
        if expected is None:
            return
        evidence = json.loads(body)
        if evidence != {"nonce": nonce, "peer": expected}:
            raise RuntimeError(f"witness nonce or egress mismatch: {evidence!r}; expected peer {expected}")


phase, kind, expected = sys.argv[1:]
if kind == "invalid":
    try:
        probe(None)
    except (OSError, RuntimeError, ValueError):
        print(json.dumps({"phase": phase, "client": kind, "rejected": True}))
    else:
        raise RuntimeError("invalid business credential reached the witness")
else:
    probe(expected)
    print(json.dumps({"phase": phase, "client": kind, "authenticated_request": True, "expected_egress": True}))
