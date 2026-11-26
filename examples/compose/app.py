"""A page that counts its own visits, the example every Compose
tutorial starts with: a web server that keeps its counter in Redis.

Only the standard library, so the image is the stock Python one and
nothing has to be built. Redis is spoken to directly: INCR is one line
each way.
"""

import http.server
import socket

REDIS = ("redis", 6379)
PORT = 8000


def visits():
    """Add one to the counter in Redis and return the new count."""
    with socket.create_connection(REDIS, timeout=5) as connection:
        connection.sendall(b"INCR hits\r\n")
        reply = connection.recv(64)

    # The reply to INCR is an integer: ":42\r\n".
    return int(reply[1:].strip())


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = f"Hello from a browser tab. I have been seen {visits()} times.\n".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    print(f"listening on :{PORT}", flush=True)
    http.server.ThreadingHTTPServer(("", PORT), Handler).serve_forever()
