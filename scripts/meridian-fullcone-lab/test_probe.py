"""Parser/negative coverage only; these tests do not perform a NAT experiment."""
import io
import socket
import struct
import unittest
from unittest.mock import patch

import probe

TXID = bytes(range(12))


def packet():
    ip = struct.unpack("!I", socket.inet_aton("192.168.240.2"))[0] ^ probe.COOKIE
    attribute = struct.pack("!HHBBHI", 0x20, 8, 0, 1, 45000 ^ (probe.COOKIE >> 16), ip)
    header = b"\x00\x00\x00\x01" + socket.inet_aton(probe.PRIMARY[0]) + struct.pack("!H", probe.PRIMARY[1])
    return header + struct.pack("!HHI", 0x101, len(attribute), probe.COOKIE) + TXID + attribute


class PartialStream:
    def __init__(self, content):
        self.content = io.BytesIO(content)

    def recv(self, size):
        return self.content.read(min(size, 1))


class EvidenceTests(unittest.TestCase):
    def test_partial_handshake(self):
        self.assertEqual(probe.read_exact(PartialStream(b"abc"), 3), b"abc")
        with self.assertRaises(probe.EvidenceError):
            probe.read_exact(PartialStream(b"ab"), 3)

    def test_mapping(self):
        result = probe.parse_response(packet(), TXID, probe.PRIMARY)
        self.assertEqual(result["mapped"], ("192.168.240.2", 45000))

    def test_reject_corrupt_evidence(self):
        valid = packet()
        invalid = [valid[:n] for n in range(len(valid))]
        for offset in (2, 3, 10, 12, 14, 18, 32, 35):
            changed = bytearray(valid)
            changed[offset] ^= 0x01
            invalid.append(bytes(changed))
        invalid.append(valid + bytes(4))
        duplicate = bytearray(valid + valid[30:])
        duplicate[12:14] = struct.pack("!H", 24)
        invalid.append(bytes(duplicate))
        for candidate in invalid:
            with self.subTest(candidate=candidate.hex()):
                with self.assertRaises(probe.EvidenceError):
                    probe.parse_response(candidate, TXID, probe.PRIMARY)
        with self.assertRaises(probe.EvidenceError):
            probe.parse_response(valid, TXID, probe.ALTERNATE)

    def test_mapping_change_rejected(self):
        results = [{"mapped": ("192.168.240.2", port)} for port in (40000, 40000, 40000, 40001)]
        with patch.object(probe, "query", side_effect=results):
            with self.assertRaises(probe.EvidenceError):
                probe.collect(None, None)

    def test_unowned_relay_rejected(self):
        fake = unittest.mock.Mock()
        fake.recvfrom.return_value = (packet(), ("192.168.240.7", 1080))
        with self.assertRaises(probe.EvidenceError):
            probe.query(fake, probe.CLIENT, probe.PRIMARY, 0, probe.PRIMARY)

    def test_missing_mapping_rejected(self):
        empty = packet()[:30]
        empty = empty[:12] + bytes(2) + empty[14:]
        with self.assertRaises(probe.EvidenceError):
            probe.parse_response(empty, TXID, probe.PRIMARY)

    def test_timeout_is_unconfirmed_not_fullcone(self):
        with patch.object(probe.sys, "argv", ["probe.py", "vless"]), \
             patch.object(probe.socket, "create_connection", side_effect=TimeoutError), \
             patch("sys.stdout", new_callable=io.StringIO) as output:
            self.assertEqual(probe.main(), 1)
            self.assertIn('"status": "unconfirmed"', output.getvalue())
            self.assertNotIn('"ordinary": true', output.getvalue())


if __name__ == "__main__":
    unittest.main()
