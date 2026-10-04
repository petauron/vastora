import socket
import ssl

# API must not be reachable from another network namespace.
try:
    connection = socket.create_connection(('192.168.240.2', 10085), 2)
except ConnectionRefusedError:
    pass
else:
    connection.close()
    raise AssertionError('Xray API unexpectedly reachable')

# Unknown SNI must be closed by HAProxy, not reach the REALITY origin.
context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
context.check_hostname = False
context.verify_mode = ssl.CERT_NONE
with socket.create_connection(('192.168.240.6', 443), 2) as connection:
    connection.settimeout(20)
    try:
        with context.wrap_socket(connection, server_hostname='unapproved.example.test'):
            raise AssertionError('unknown SNI completed a TLS handshake')
    except (ssl.SSLError, ConnectionResetError):
        pass
print('PASS: loopback API isolation and unknown SNI rejection')
