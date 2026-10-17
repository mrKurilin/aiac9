"""Real installer with fake system commands; no installation or network."""
import os
from pathlib import Path
import pty
import select
import subprocess
import tempfile
import time
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'deploy/bootstrap-vps.sh'


class DeployTests(unittest.TestCase):
    def test_bash_syntax(self):
        subprocess.run(['bash', '-n', str(SCRIPT)], check=True)

    def run_installer(self, unhealthy=False):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            calls = base / 'calls.log'
            stubs = {
                'uname': '#!/bin/sh\necho Linux\n',
                'id': '#!/bin/sh\necho 0\n',
                'systemctl': '#!/bin/sh\nexit 0\n',
                'docker': '''#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_CALLS"
case "$1" in
 inspect) echo "$MOCK_HEALTH" ;;
 *) exit 0 ;;
esac
''',
                'sleep': '#!/bin/sh\nexit 0\n',
                'apt-get': '#!/bin/sh\nexit 99\n',
            }
            for name, content in stubs.items():
                path = base / name
                path.write_text(content)
                path.chmod(0o755)
            pid, master = pty.fork()
            if pid == 0:
                environment = dict(os.environ)
                environment.update(PATH=str(base) + ':' + environment['PATH'],
                                   MOCK_CALLS=str(calls), MOCK_HEALTH='unhealthy' if unhealthy else 'healthy')
                os.execvpe('bash', ['bash', '-x', str(SCRIPT)], environment)
            output = bytearray()
            prompts = [
                ('Токен Telegram-бота', b'12345:placeholder_telegram_value'),
                ('API-токен Travelpayouts', b'placeholder_travel_value'),
                ('API-ключ DeepSeek', b'placeholder_deepseek_value'),
                ('Разрешённые Telegram ID', b'12345'),
            ]
            index = 0
            deadline = time.monotonic() + 20
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
                _, status = os.waitpid(pid, 0)
            finally:
                os.close(master)
                if status is None:
                    os.kill(pid, 15)
                    os.waitpid(pid, 0)
            text = output.decode(errors='replace')
            self.assertEqual(index, 4, text)
            for _, value in prompts[:3]:
                self.assertNotIn(value.decode(), text, 'secret echoed')
            command_log = calls.read_text()
            for _, value in prompts[:3]:
                self.assertNotIn(value.decode(), command_log, 'secret in command arguments')
            return os.waitstatus_to_exitcode(status), text, command_log

    def test_install_healthcheck_and_no_secret_output(self):
        status, text, calls = self.run_installer()
        self.assertEqual(status, 0, text)
        self.assertIn('Готово: бот запущен', text)
        self.assertIn('--restart unless-stopped', calls)
        self.assertIn('--env-file', calls)
        self.assertIn('rm mrkai-flights-previous-', calls)

    def test_failed_health_restores_previous_container(self):
        status, text, calls = self.run_installer(unhealthy=True)
        self.assertNotEqual(status, 0)
        self.assertNotIn('Готово: бот запущен', text)
        self.assertIn('start mrkai-flights', calls)
        self.assertIn('rm -f mrkai-flights', calls)


if __name__ == '__main__':
    unittest.main()
