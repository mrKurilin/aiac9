import http.server
import json
import os
import pty
import select
import subprocess
import termios
import threading
import time


started = threading.Event()


class SlowModel(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        started.set()
        time.sleep(5)
        try:
            payload = json.dumps({"message": {"role": "assistant", "content": "OK"}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(payload)
        except BrokenPipeError:
            pass

    def log_message(self, *_args):
        pass


server = http.server.HTTPServer(("127.0.0.1", 0), SlowModel)
threading.Thread(target=server.serve_forever, daemon=True).start()
environment = os.environ.copy()
environment["OLLAMA_BASE_URL"] = "http://127.0.0.1:" + str(server.server_port)


def session():
    master, slave = pty.openpty()
    initial = termios.tcgetattr(slave)
    process = subprocess.Popen(["./mrkai"], stdin=slave, stdout=slave, stderr=slave, env=environment)
    output = bytearray()

    def read_for(seconds):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.05)
            if ready:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break

    def expect(value, seconds=10):
        deadline = time.monotonic() + seconds
        while value not in output and time.monotonic() < deadline:
            read_for(0.2)
        assert value in output, repr(output.decode("utf-8", "replace")[-1000:])

    try:
        expect(b"/help", 15)
        assert process.poll() is None
        assert b"\x1b[?25h" in output, "cursor not visible"
        os.write(master, b"/server st\t\r")
        expect("Сервис запущен".encode())
        os.write(master, b"/server status\r")
        expect("Сервис работает".encode())
        os.write(master, "тест два".encode() + b"\x1b\x7f")
        read_for(0.3)
        assert "тест ".encode() in output, "Unicode word deletion failed"
        os.write(master, b"\x15")  # Ctrl+U clears the editor.
        os.write(master, b"\x1b[27u")  # Extended key.
        os.write(master, b"\x1b[<64;1;1M")  # Mouse wheel.
        os.write(master, b"/check\r")
        deadline = time.monotonic() + 5
        while not started.is_set() and time.monotonic() < deadline:
            read_for(0.2)
        assert started.is_set(), "fake slow request did not start"
        os.write(master, b"\x1b[27u")
        read_for(0.15)
        os.write(master, b"\x1b[27u")
        read_for(0.6)
        assert process.poll() is not None, "double Esc did not exit"
        assert "останавливаю".encode() in output, "cancellation log missing"
        after = termios.tcgetattr(slave)
        for index in (0, 1, 2):
            assert initial[index] == after[index], "terminal flags not restored"
        print("PTY: idle, cursor, nested completion, Unicode edit, mouse/key events, cancellation, exit and restoration OK")
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=3)
        os.close(master)
        os.close(slave)


try:
    session()
finally:
    server.shutdown()
