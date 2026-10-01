"""Run the normal mrkai entrypoint in a PTY against a local fake chat API."""

import http.server
import json
import os
import pty
import select
import subprocess
import tempfile
import threading
import time


class ChatHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.dumps({"choices": [{"message": {"content": "25 days per year"}}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


def main():
    server = http.server.HTTPServer(("127.0.0.1", 0), ChatHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    master, slave = pty.openpty()
    env = os.environ.copy()
    env["DATA_DIR"] = tempfile.mkdtemp(prefix="aiac9-d24-rag-")
    env["DEEPSEEK_API_KEY"] = "placeholder"
    env["DEEPSEEK_BASE_URL"] = "http://127.0.0.1:" + str(server.server_port)
    process = subprocess.Popen(["./mrkai"], stdin=slave, stdout=slave, stderr=slave, env=env)
    output = bytearray()

    def read_until(needle, seconds):
        deadline = time.monotonic() + seconds
        while needle not in output and time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.05)
            if ready:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break
        assert needle in output, "expected terminal milestone missing"

    try:
        read_until(b"/help", 15)
        os.write(master, b"/index build knowledge\r")
        read_until("Индекс: готово".encode(), 5)
        os.write(master, b"/rag on\r")
        read_until("RAG включён".encode(), 5)
        os.write(master, b"How many PTO days do employees with 3-5 years of service receive?\r")
        read_until(b"3-5 years of service: 25 days per year", 8)
        assert b"Employee_Handbook_2025.txt" in output
        assert "Источники и цитаты".encode() in output
        os.write(master, b"/exit\r")
        deadline = time.monotonic() + 3
        while process.poll() is None and time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.05)
            if ready:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break
        assert process.poll() is not None, "entrypoint did not exit"
        print("PTY RAG: answer, source, and verbatim quote OK")
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=3)
        server.shutdown()
        os.close(master)
        os.close(slave)


if __name__ == "__main__":
    main()
