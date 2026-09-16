"""Seeds a fresh n8n instance through its own REST API: an owner account
(user management can't be disabled in n8n's current major version), an AWS
credential pointed at nothing but the real hostnames, and the demo
workflow, then publishes it so its webhook goes live.
"""

import json
import sys
import time
import urllib.error
import urllib.request

BASE = "http://n8n_main:5678"


def post(path, body, cookie=None):
    req = urllib.request.Request(
        BASE + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    if cookie:
        req.add_header("Cookie", cookie)
    with urllib.request.urlopen(req) as resp:
        set_cookie = resp.headers.get("Set-Cookie")
        raw = resp.read()
    return (json.loads(raw) if raw else None), set_cookie


def post_with_retry(path, body, attempts=20, delay=2):
    for attempt in range(attempts):
        try:
            return post(path, body)
        except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError):
            if attempt == attempts - 1:
                raise
            time.sleep(delay)


def main():
    # Retries: n8n answers "n8n is starting up. Please wait" with a 200 for
    # a while after /rest/settings itself starts responding.
    _, set_cookie = post_with_retry(
        "/rest/owner/setup",
        {
            "email": "admin@example.com",
            "firstName": "Kevin",
            "lastName": "Admin",
            "password": "KevinDemo123!",
        },
    )
    cookie = set_cookie.split(";", 1)[0]

    cred, _ = post(
        "/rest/credentials",
        {
            "name": "MiniStack AWS",
            "type": "aws",
            "data": {
                "region": "us-east-1",
                "accessKeyId": "test",
                "secretAccessKey": "test",
            },
        },
        cookie,
    )
    cred_id = cred["data"]["id"]
    print("created credential " + cred_id)

    with open("/data/workflow.json") as f:
        workflow = f.read().replace("__AWS_CREDENTIAL_ID__", cred_id)

    wf, _ = post("/rest/workflows", json.loads(workflow), cookie)
    wf_id = wf["data"]["id"]
    version_id = wf["data"]["versionId"]
    print("created workflow " + wf_id)

    post(f"/rest/workflows/{wf_id}/activate", {"versionId": version_id}, cookie)
    print("activated workflow " + wf_id)


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as e:
        print(e.read().decode(), file=sys.stderr)
        raise
