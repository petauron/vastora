"""Synthetic TLS target and HTTP witness inside the private lab network."""
import http.server
import json
import ssl
import threading


class Witness(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"nonce": self.path.removeprefix("/"), "peer": self.client_address[0]}).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


httpd = http.server.ThreadingHTTPServer(("0.0.0.0", 8080), Witness)
threading.Thread(target=httpd.serve_forever, daemon=True).start()
target = http.server.ThreadingHTTPServer(("0.0.0.0", 443), Witness)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.minimum_version = ssl.TLSVersion.TLSv1_3
context.load_cert_chain("/lab/cert.pem", "/lab/key.pem")
context.set_alpn_protocols(["h2", "http/1.1"])
target.socket = context.wrap_socket(target.socket, server_side=True)
target.serve_forever()
