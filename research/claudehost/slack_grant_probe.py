"""Offline #68 experiment. Run with python3 slack_grant_probe.py /path/to/claude."""

import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import uuid


URI = "ui://fixture/scope"
SCOPE = {"actor": "fixture-user", "workspace": "fixture-workspace", "channel": "fixture-channel"}
TOOL = "slack_send_message"


def serve():
    instance = uuid.uuid4().hex
    for line in sys.stdin:
        request = json.loads(line)
        method = request.get("method")
        if "id" not in request:
            continue
        with Path("ledger").open("a") as ledger:
            print(json.dumps([instance, Path("epoch").read_text(), method]), file=ledger)
        if method == "initialize":
            result = {"protocolVersion": request["params"]["protocolVersion"],
                      "serverInfo": {"name": "fixture", "version": "1"},
                      "capabilities": {"tools": {}, "resources": {}}}
        elif method == "tools/list":
            result = {"tools": [{"name": TOOL, "inputSchema": {"type": "object"},
                                 "_meta": {"ui": {"resourceUri": URI}}}]}
        elif method == "resources/list":
            result = {"resources": [{"name": "scope", "uri": URI}]}
        elif method == "resources/read" and request["params"]["uri"] == URI:
            result = {"contents": [{"uri": URI, "mimeType": "text/html;profile=mcp-app",
                                    "text": json.dumps(SCOPE)}]}
        elif method == "ping":
            result = {}
        else:
            reply = {"error": {"code": -32601, "message": "unsupported fixture method"}}
            print(json.dumps({"jsonrpc": "2.0", "id": request["id"], **reply}), flush=True)
            continue
        print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}), flush=True)


def probe(binary, directory):
    check = unittest.TestCase()
    script = str(Path(__file__).resolve())
    server = {"command": sys.executable, "args": [script, "--fixture"]}
    config = {"mcpServers": {"fixture": server}}
    env = {"HOME": str(directory), "CLAUDE_CONFIG_DIR": str(directory / "config"),
           "TMPDIR": str(directory), "PATH": os.defpath, "ANTHROPIC_BASE_URL": "http://127.0.0.1:1",
           "ANTHROPIC_API_KEY": "fixture-placeholder", "CLAUDE_CODE_MAX_RETRIES": "0",
           "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
           "ENABLE_CLAUDEAI_MCP_SERVERS": "false"}
    args = [binary, "--bare", "-p", "--verbose", "--input-format", "stream-json",
            "--output-format", "stream-json", "--strict-mcp-config", "--mcp-config",
            json.dumps(config), "--setting-sources", "", "--no-session-persistence",
            "--tools", "", "--no-chrome"]
    epoch = directory / "epoch"
    epoch.write_text("A")
    with subprocess.Popen(args, cwd=directory, env=env, text=True,
                          stdin=subprocess.PIPE, stdout=subprocess.PIPE) as proc:
        events = queue.Queue()

        def read_output():
            for line in proc.stdout:
                events.put(line)
            events.put(None)

        def control(subtype, **fields):
            request_id = uuid.uuid4().hex
            request = {"type": "control_request", "request_id": request_id,
                       "request": {"subtype": subtype, **fields}}
            print(json.dumps(request), file=proc.stdin, flush=True)
            deadline = time.monotonic() + 45
            while time.monotonic() < deadline:
                try:
                    line = events.get(timeout=max(0, deadline - time.monotonic()))
                except queue.Empty:
                    raise TimeoutError(subtype) from None
                if line is None:
                    raise RuntimeError(f"host exited during {subtype}")
                event = json.loads(line)
                reply = event.get("response", {})
                if event["type"] == "control_response" and reply["request_id"] == request_id:
                    if reply["subtype"] != "success":
                        raise RuntimeError(f"{subtype}: {reply}")
                    return reply.get("response", {})
            raise TimeoutError(subtype)

        def snapshot():
            for _ in range(50):
                rows = control("mcp_status")["mcpServers"]
                if len(rows) == 1 and rows[0]["name"] == "fixture" and rows[0]["status"] == "connected":
                    break
                time.sleep(0.1)
            else:
                raise RuntimeError("expected a single connected fixture")
            check.assertEqual([tool["name"] for tool in rows[0]["tools"]], [TOOL])
            resource = control("mcp_read_resource", serverName="fixture", uri=URI)
            check.assertEqual(json.loads(resource["contents"][0]["text"]), SCOPE)
            return rows, resource

        threading.Thread(target=read_output, daemon=True).start()
        try:
            control("initialize")
            before = snapshot()
            epoch.write_text("B")
            check.assertEqual(snapshot(), before, "public evidence changed at mock grant replacement")
            control("mcp_reconnect", serverName="fixture")
            check.assertEqual(snapshot(), before, "public evidence changed at explicit reconnect")
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()

    ledger = [json.loads(line) for line in (directory / "ledger").read_text().splitlines()]
    reads = [(instance, epoch) for instance, epoch, method in ledger if method == "resources/read"]
    check.assertEqual([epoch for _, epoch in reads], ["A", "B", "B"])
    check.assertEqual(reads[0][0], reads[1][0])
    check.assertNotEqual(reads[1][0], reads[2][0])
    check.assertNotIn("tools/call", [method for _, _, method in ledger])
    print("3 identical host snapshots; mock epochs A/B/B; 2 provider processes; 0 tool calls")


if __name__ == "__main__":
    if sys.argv[1:] == ["--fixture"]:
        serve()
    elif len(sys.argv) == 2:
        with tempfile.TemporaryDirectory() as temp:
            probe(str(Path(sys.argv[1]).resolve()), Path(temp))
    else:
        sys.exit(__doc__)
