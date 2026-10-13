"""Publish a validated decision as a GitHub review of the analyzed commit."""

import json
import os
import re
import subprocess
import sys


EVENTS = {"approve": "APPROVE", "request_changes": "REQUEST_CHANGES", "comment": "COMMENT"}


def publish(result, repository, number, head):
    if not isinstance(result, dict) or set(result) != {"decision", "body"}:
        raise ValueError("Invalid review structure")
    decision, body = result["decision"], result["body"]
    if not isinstance(decision, str) or decision not in EVENTS:
        raise ValueError("Invalid review decision")
    if not isinstance(body, str) or not body.strip():
        raise ValueError("Empty review body")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid repository")
    if not re.fullmatch(r"[1-9][0-9]*", number) or not re.fullmatch(r"[a-f0-9]{40}", head):
        raise ValueError("Invalid PR number or commit")
    payload = {"event": EVENTS[decision], "body": body, "commit_id": head}
    print("GitHub: submitting " + payload["event"], flush=True)
    subprocess.run(
        ["gh", "api", "--method", "POST", f"repos/{repository}/pulls/{number}/reviews", "--input", "-"],
        input=json.dumps(payload), text=True, check=True, stdout=subprocess.DEVNULL,
    )
    print("GitHub: review published", flush=True)


if __name__ == "__main__":
    try:
        with open(sys.argv[1], encoding="utf-8") as source:
            result = json.load(source)
        publish(result, os.environ["GITHUB_REPOSITORY"], os.environ["PR_NUMBER"], os.environ["HEAD_SHA"])
    except (ValueError, KeyError, IndexError, OSError, subprocess.CalledProcessError):
        print("GitHub: review publication failed; check Actions permissions and the review result", file=sys.stderr)
        sys.exit(1)
