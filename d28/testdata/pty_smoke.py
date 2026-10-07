"""Exercise the normal mrkai launcher in a PTY without model or paid API calls."""

import os
import pty
import select
import subprocess
import termios
import time
from pathlib import Path


def main():
    master, slave = pty.openpty()
    before = termios.tcgetattr(slave)
    child_env = os.environ.copy()
    child_env.pop("DEEPSEEK_API_KEY", None)
    app_dir = Path(__file__).resolve().parents[1]
    proc = subprocess.Popen(
        ["./mrkai"], stdin=slave, stdout=slave, stderr=slave,
        cwd=app_dir, env=child_env,
    )
    output = bytearray()

    def read_until(fragment, timeout=12):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if fragment in output:
                return True
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break
        return fragment in output

    try:
        assert read_until("день 28".encode()), "interactive title is missing"
        assert b"\x1b[?25h" in output, "cursor is not visible"
        time.sleep(0.2)
        assert proc.poll() is None, "agent did not wait for input"
        os.write(master, "/comp\t вопрос\r".encode())
        assert read_until(b"DEEPSEEK_API_KEY"), "completion did not submit /compare"
        os.write(master, "тест слово\x17".encode())
        os.write(master, b"\x1b[<64;10;5M\x1b[27u")
        assert read_until("Нажмите Esc".encode(), 2), "first Esc was not handled"
        time.sleep(0.2)
        os.write(master, b"\x1b[27u")
        deadline = time.monotonic() + 5
        while proc.poll() is None and time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    pass
        assert proc.poll() == 0, "second Esc did not exit cleanly"
        after = termios.tcgetattr(slave)
        restored = termios.ICANON | termios.ECHO | termios.ISIG
        assert after[3] & restored == before[3] & restored, "input modes were not restored"
        print("PTY: idle, cursor, completion, Unicode/word key, mouse, extended Esc, double Esc, restoration OK")
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()
        os.close(master)
        os.close(slave)


if __name__ == "__main__":
    main()
