import io
import json
import pathlib
import sqlite3
import tempfile
import unittest

from observer import Observer, Server, redact


class ObserverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.db = self.root / "ledger.db"
        self.writer = sqlite3.connect(self.db)
        self.addCleanup(self.writer.close)
        self.writer.executescript("""
        PRAGMA journal_mode=WAL;
        CREATE TABLE repos(id TEXT, root_path TEXT);
        CREATE TABLE messages(id INTEGER PRIMARY KEY, repo_id TEXT, ts INT, agent_id TEXT, thread TEXT, kind TEXT, body TEXT);
        CREATE TABLE claims(repo_id TEXT,item_id TEXT,agent_id TEXT,state TEXT,generation INT,claimed_at INT,last_touch INT,intent TEXT);
        CREATE TABLE dispatch_reservations(repo_id TEXT,item_id TEXT,canonical_item_id TEXT,source_ref TEXT,reserved_by TEXT,state TEXT,generation INT,worker_thread_id TEXT,updated_at INT);
        CREATE TABLE execution_authorizations(repo_id TEXT,id TEXT,item_id TEXT,holder TEXT,state TEXT,run_id INT,run_attempt INT,last_step TEXT,updated_at INT,reconciliation TEXT);
        CREATE TABLE items(repo_id TEXT,item_id TEXT,title TEXT,status TEXT,updated_at INT,archived INT);
        CREATE TABLE attestations(repo_id TEXT,item_id TEXT,id INT,kind TEXT,exit_code INT,output_hash TEXT,created_at INT,agent_id TEXT);
        """)
        self.writer.execute("INSERT INTO repos VALUES ('r',?)", (str(self.repo),))
        self.writer.execute("INSERT INTO claims VALUES ('r','TASK-1','worker','held',1,1,2,'work')")
        self.writer.execute("INSERT INTO dispatch_reservations VALUES ('r','DISPATCH-1','TASK-1','github:o/r#1','dispatcher','dispatched',1,'session',1)")
        self.writer.execute("INSERT INTO items VALUES ('r','TASK-1','Example','open',1,0)")
        for i in range(1, 4):
            self.writer.execute("INSERT INTO messages VALUES (?,'r',?,'worker','TASK-1','say',?)", (i, i, f"progress {i}"))
        self.writer.execute("INSERT INTO messages VALUES (4,'other',4,'private','TASK-2','say','other repo private')")
        self.writer.commit()
        self.observer = Observer(self.db, self.repo)

    def test_read_only_and_no_mutations_across_tools(self):
        before = list(self.writer.iterdump())
        self.observer.snapshot({})
        self.observer.changes_since({"message_id": 0})
        self.observer.item_checkpoint({"item_id": "TASK-1"})
        with self.observer.read() as (c, _):
            with self.assertRaises(sqlite3.OperationalError):
                c.execute("UPDATE claims SET last_touch=999")
            with self.assertRaises(sqlite3.OperationalError):
                c.execute("CREATE TABLE mutation(id INT)")
        self.assertEqual(before, list(self.writer.iterdump()))

    def test_wal_is_live_and_cursor_never_skips_page(self):
        self.writer.execute("INSERT INTO messages VALUES (5,'r',5,'worker','TASK-1','say','new WAL record')")
        self.writer.commit()
        first = self.observer.changes_since({"message_id": 0, "limit": 2})
        self.assertEqual(first["max_message_id"], 5)
        self.assertEqual(first["next_message_id"], 2)
        self.assertTrue(first["has_more"])
        second = self.observer.changes_since({"message_id": 2, "limit": 2})
        self.assertEqual([m["id"] for m in second["messages"]], [3, 5])
        self.assertFalse(second["has_more"])
        self.assertEqual(second["next_message_id"], 5)

    def test_metadata_default_and_opt_in_redaction(self):
        self.writer.execute("UPDATE messages SET body=? WHERE id=1", ('api_key="dont-show-this" Bearer ABC123 ghp_abcdefghijklmnop',))
        self.writer.commit()
        result = self.observer.changes_since({"message_id": 0})
        self.assertNotIn("body", result["messages"][0])
        result = self.observer.changes_since({"message_id": 0, "include_text": True})
        text = result["messages"][0]["body"]
        for secret in ("dont-show-this", "ABC123", "ghp_abcdefghijklmnop"):
            self.assertNotIn(secret, text)
        self.assertEqual(redact("normal checkpoint"), "normal checkpoint")

    def test_repository_isolation_and_missing_root(self):
        self.assertNotIn("other repo private", json.dumps(self.observer.changes_since({"message_id": 0, "include_text": True})))
        missing = self.root / "missing-repo"
        missing.mkdir()
        with self.assertRaisesRegex(ValueError, "not registered"):
            Observer(self.db, missing).snapshot({})
        with self.assertRaises(FileNotFoundError):
            Observer(self.root / "missing.db", self.repo)
        self.assertFalse((self.root / "missing.db").exists())

    def test_quoted_and_multiline_secrets_redacted_before_truncation(self):
        secret = 'password="with spaces secret" token=ABC123&safe=1\n-----BEGIN PRIVATE KEY-----\n' + 'private material\n' * 200 + '-----END PRIVATE KEY-----'
        self.writer.execute("UPDATE messages SET body=? WHERE id=1", (secret,))
        self.writer.commit()
        result = self.observer.changes_since({"message_id": 0, "include_text": True})
        body = result["messages"][0]["body"]
        self.assertNotIn("with spaces", body)
        self.assertNotIn("ABC123", body)
        self.assertNotIn("private material", body)
        self.assertIn("safe=1", body)

    def test_claim_touch_not_treated_as_checkpoint(self):
        self.writer.execute("INSERT INTO messages VALUES (6,'r',6,'worker','TASK-1','claim','heartbeat only')")
        self.writer.commit()
        result = self.observer.item_checkpoint({"item_id": "TASK-1"})
        self.assertEqual(result["checkpoints"][0]["id"], 3)
        self.assertTrue(result["indexed_item_may_be_stale"])
        self.assertFalse(result["worker_runtime_verified"])

    def test_invalid_arguments_and_injection(self):
        for value in (True, -1, "1", 2**63):
            with self.assertRaises(ValueError):
                self.observer.changes_since({"message_id": value})
        with self.assertRaises(ValueError):
            self.observer.changes_since({"message_id": 99})
        with self.assertRaises(ValueError):
            self.observer.item_checkpoint({"item_id": "../secrets"})
        with self.assertRaises(ValueError):
            self.observer.execution_receipt({"execution_id": "x' OR 1=1 --"})

    def test_receipt_allowlist_and_symlink_escape(self):
        evidence = self.root / "evidence"
        receipt = evidence / "run-1"
        receipt.mkdir(parents=True)
        data = {"status": "passed", "release_sha": "sha", "ok": True, "secret": "NEVER_RETURN", "password": "NEVER_RETURN"}
        for name in ("operation-summary.json", "candidate-rollout.json", "public-acceptance-health.json"):
            (receipt / name).write_text(json.dumps(data))
        self.writer.execute("INSERT INTO execution_authorizations VALUES ('r','exec-1','ENV-2','deployer','reconciled',1,1,'execution-acceptance',9,?)", (str(receipt),))
        self.writer.commit()
        obs = Observer(self.db, self.repo, evidence)
        before = list(self.writer.iterdump())
        output = obs.execution_receipt({"execution_id": "exec-1"})
        self.assertNotIn("NEVER_RETURN", json.dumps(output))
        self.assertEqual(output["receipts"]["candidate-rollout.json"]["status"], "passed")
        self.assertEqual(before, list(self.writer.iterdump()))
        outside = self.root / "outside.json"
        outside.write_text('{"status":"SECRET"}')
        (receipt / "candidate-rollout.json").unlink()
        (receipt / "candidate-rollout.json").symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "escapes"):
            obs.execution_receipt({"execution_id": "exec-1"})

    def test_stdio_lifecycle_and_write_tools_absent(self):
        server = Server(self.observer)
        frames = [
            {"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
            {"jsonrpc": "2.0", "id": 2, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 3, "method": "tools/list"},
            {"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "squad_claim", "arguments": {}}},
            {"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": {"name": "squad_observer_changes_since", "arguments": {"message_id": 0, "sql": "DELETE FROM messages"}}},
            {"jsonrpc": "2.0", "id": 6, "method": "tools/call", "params": {"name": "squad_observer_snapshot", "arguments": {}}},
        ]
        source = io.StringIO("\n".join(json.dumps(f) for f in frames) + "\n")
        target = io.StringIO()
        server.serve(source, target)
        responses = [json.loads(line) for line in target.getvalue().splitlines()]
        self.assertEqual(len(responses), 6)  # Notifications receive no response.
        self.assertEqual(responses[0]["error"]["code"], -32000)
        tools = responses[2]["result"]["tools"]
        self.assertEqual(len(tools), 4)
        self.assertTrue(all(t["annotations"]["readOnlyHint"] for t in tools))
        self.assertEqual(responses[3]["error"]["code"], -32602)
        self.assertEqual(responses[4]["error"]["code"], -32602)
        self.assertFalse(responses[5]["result"]["isError"])

    def test_bad_json_and_large_frame(self):
        source = io.StringIO('bad json\n' + 'x' * 70000 + '\n{"jsonrpc":"2.0","id":1,"method":"ping"}\n')
        target = io.StringIO()
        Server(self.observer).serve(source, target)
        output = [json.loads(line) for line in target.getvalue().splitlines()]
        self.assertEqual([r.get("error", {}).get("code") for r in output], [-32700, -32600, None])


if __name__ == "__main__":
    unittest.main()
