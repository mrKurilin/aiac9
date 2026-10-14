import http.server
import json
import os
import pty
import select
import subprocess
import termios
import fcntl
import struct
import threading
import time
import tempfile
from pathlib import Path


binary_dir = tempfile.TemporaryDirectory(prefix="aiac9-d33-pty-")
binary = str(Path(binary_dir.name) / "mrkai")
subprocess.run(["go", "build", "-o", binary, "."], check=True)
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


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), SlowModel)
threading.Thread(target=server.serve_forever, daemon=True).start()
environment = os.environ.copy()
environment["DEEPSEEK_API_KEY"] = "placeholder"
environment["DEEPSEEK_BASE_URL"] = "http://127.0.0.1:" + str(server.server_port)


def session(exit_key):
    started.clear()
    master, slave = pty.openpty()
    initial = termios.tcgetattr(slave)
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 28, 90, 0, 0))
    process = subprocess.Popen([binary], stdin=slave, stdout=slave, stderr=slave, env=environment)
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
        expect("поддержка пользователей".encode(), 15)
        read_for(0.2)
        assert process.poll() is None, "idle terminal exited"
        assert b"\x1b[?25h" in output, "cursor not visible"
        os.write(master, b"/mcp to\t\r")
        expect(b"support_ticket")
        for prefix, suffix, expected in [
            (b"/he", b"", "Команды:"),
            (b"/ti", b" t-101", "Тикет t-101"),
            (b"/su", " неизвестный Почему?".encode(), "тикет не найден"),
            (b"/index bu", b"", "Индекс готов:"),
            (b"/index st", b"", "Индекс:"),
            (b"/res", b"", "Новый диалог"),
            (b"/cl", b"", "Новый диалог"),
        ]:
            output.clear()
            os.write(master, prefix + b"\t" + suffix + b"\r")
            expect(expected.encode())
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 35, 110, 0, 0))
        os.write(master, b"\x1bs\x1bs")
        os.write(master, "тест два".encode() + b"\x1b\x7f")
        read_for(0.3)
        assert "тест ".encode() in output, "Unicode word deletion failed"
        os.write(master, b"\x15")
        os.write(master, b"\x1b[27u")
        os.write(master, b"\x1b[<64;1;1M")
        os.write(master, "Почему не работает авторизация?\r".encode())
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
        for index in (0, 1, 2, 3):
            # macOS sets PENDIN when canonical input is restored; it is transient.
            mask = ~getattr(termios, "PENDIN", 0) if index == 3 else -1
            assert initial[index] & mask == after[index] & mask, f"terminal flags not restored at {index}"
        for index in (4, 5):
            assert initial[index] == after[index], "terminal speeds not restored"
        for control in (termios.VMIN, termios.VTIME):
            assert initial[6][control] == after[6][control], "terminal timing not restored"
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=3)
        os.close(master)
        os.close(slave)


try:
    session(b"\x1b[27u")
    session(b"\x03")
    for prefix in (b"/ex", b"/qu"):
        master, slave = pty.openpty()
        process = subprocess.Popen([binary], stdin=slave, stdout=slave, stderr=slave, env=environment)
        try:
            deadline = time.monotonic() + 10
            ready = bytearray()
            while "поддержка пользователей".encode() not in ready and time.monotonic() < deadline:
                if select.select([master], [], [], 0.1)[0]:
                    ready.extend(os.read(master, 65536))
            assert "поддержка пользователей".encode() in ready
            os.write(master, prefix + b"\t\r")
            deadline = time.monotonic() + 3
            while process.poll() is None and time.monotonic() < deadline:
                if select.select([master], [], [], 0.05)[0]:
                    os.read(master, 65536)
            assert process.poll() is not None, "completed exit command hung"
            assert process.returncode == 0, "completed exit command failed"
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=3)
            os.close(master)
            os.close(slave)
    print("PTY: idle, cursor, nested completion, Unicode edit, mouse/key events, cancellation, double Esc/Ctrl+C and restoration OK")
finally:
    server.shutdown()
    binary_dir.cleanup()
