"""Check terminal startup prompts with a fake Go command, without external APIs."""
import os
from pathlib import Path
import pty
import select
import subprocess
import tempfile
import time
import unittest

LAUNCHER = Path(__file__).resolve().parents[1] / 'mrkai'


class LauncherTests(unittest.TestCase):
    def test_interactive_start_asks_only_two_keys_and_does_not_echo(self):
        with tempfile.TemporaryDirectory() as temp:
            fake_go = Path(temp) / 'go'
            fake_go.write_text('''#!/bin/sh
[ -n "$TRAVELPAYOUTS_TOKEN" ] || exit 2
[ -n "$DEEPSEEK_API_KEY" ] || exit 3
[ -z "${TELEGRAM_BOT_TOKEN:-}" ] || exit 4
printf 'TERMINAL_STARTED\\n'
''')
            fake_go.chmod(0o755)
            pid, master = pty.fork()
            if pid == 0:
                environment = dict(os.environ)
                for name in ('TRAVELPAYOUTS_TOKEN', 'DEEPSEEK_API_KEY', 'TELEGRAM_BOT_TOKEN'):
                    environment.pop(name, None)
                environment['PATH'] = temp + ':' + environment['PATH']
                os.execvpe('bash', ['bash', '-x', str(LAUNCHER)], environment)
            output = bytearray()
            prompts = [('API-токен Travelpayouts', b'placeholder_travel_value'),
                       ('API-ключ DeepSeek', b'placeholder_deepseek_value')]
            index = 0
            deadline = time.monotonic() + 10
            status = None
            try:
                while time.monotonic() < deadline:
                    ready, _, _ = select.select([master], [], [], 0.05)
                    if ready:
                        try:
                            chunk = os.read(master, 65536)
                        except OSError:
                            break
                        if not chunk:
                            break
                        output.extend(chunk)
                    if index < len(prompts) and prompts[index][0].encode() in output:
                        time.sleep(0.05)
                        os.write(master, prompts[index][1] + b'\n')
                        index += 1
                done, status = os.waitpid(pid, os.WNOHANG)
                finish_deadline = time.monotonic() + 1
                while done == 0 and time.monotonic() < finish_deadline:
                    time.sleep(0.02)
                    done, status = os.waitpid(pid, os.WNOHANG)
                if done == 0:
                    os.kill(pid, 15)
                    _, status = os.waitpid(pid, 0)
                    self.fail('launcher did not finish')
            finally:
                os.close(master)
                if status is None:
                    os.kill(pid, 15)
                    os.waitpid(pid, 0)
            text = output.decode(errors='replace')
            self.assertEqual(os.waitstatus_to_exitcode(status), 0, text)
            self.assertEqual(index, 2, text)
            self.assertIn('TERMINAL_STARTED', text)
            self.assertNotIn('Telegram', text)
            for _, value in prompts:
                self.assertNotIn(value.decode(), text)

    def test_pipe_without_keys_fails_with_named_setting(self):
        environment = dict(os.environ)
        environment.pop('TRAVELPAYOUTS_TOKEN', None)
        environment.pop('DEEPSEEK_API_KEY', None)
        result = subprocess.run(['bash', str(LAUNCHER)], input='', text=True,
                                capture_output=True, env=environment)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('TRAVELPAYOUTS_TOKEN', result.stderr)
        self.assertNotIn('Telegram', result.stderr)


if __name__ == '__main__':
    unittest.main()
