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
            payload = json.dumps({"choices": [{"message": {"role": "assistant", "content": "OK"}}]}).encode()
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
environment["DEEPSEEK_API_KEY"] = "placeholder"
environment["DEEPSEEK_BASE_URL"] = "http://127.0.0.1:" + str(server.server_port)


def session(exit_key):
    started.clear()
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
        assert value in output, repr(output.decode("utf-8", "replace")[-1500:])

    try:
        expect("ревью кода".encode(), 15)
        read_for(0.2)
        assert process.poll() is None, "idle terminal exited"
        assert b"\x1b[?25h" in output, "cursor not visible"
        os.write(master, b"/index st\t\r")
        expect("Индекс не готов".encode())
        for command in ("/help", "/review", "/index", "/index build", "/index status", "/reset", "/clear", "/exit", "/quit"):
            # Select every command through Tab without submitting destructive exit commands.
            output.clear()
            os.write(master, command[:-1].encode() + b"\t")
            read_for(0.2)
            assert command.encode() in output, "completion missing: " + command
            os.write(master, b"\x15")
            read_for(0.1)
        os.write(master, "тест два".encode() + b"\x1b\x7f")
        read_for(0.3)
        assert "тест ".encode() in output, "Unicode word deletion failed"
        os.write(master, b"\x15")
        os.write(master, b"\x1b[27u")
        os.write(master, b"\x1b[<64;1;1M")
        os.write(master, "Что делает Agent?\r".encode())
        deadline = time.monotonic() + 6
        while not started.is_set() and time.monotonic() < deadline:
            read_for(0.2)
        assert started.is_set(), "fake slow request did not start"
        os.write(master, exit_key)
        read_for(0.15)
        os.write(master, exit_key)
        read_for(0.7)
        assert process.poll() is not None, "double cancel did not exit"
        assert "останавливаю".encode() in output, "cancellation log missing"
        after = termios.tcgetattr(slave)
        for index in (0, 1, 2):
            assert initial[index] == after[index], "terminal flags not restored"
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=3)
        os.close(master)
        os.close(slave)


try:
    session(b"\x1b[27u")
    session(b"\x03")
    print("PTY: idle, cursor, nested completion, Unicode edit, mouse/key events, cancellation, double Esc/Ctrl+C and restoration OK")
finally:
    server.shutdown()
