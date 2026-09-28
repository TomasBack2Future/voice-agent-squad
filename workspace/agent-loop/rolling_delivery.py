#!/usr/bin/env python3
"""Read-only rolling merge/acceptance plan from a fresh, independently verified snapshot.

Squad decisions and claims remain authority. This planner never dispatches,
merges, changes assignments, closes reservations or acquires an environment.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import re

INTEGRATED = {"integrated", "accepted", "closed"}
STATES = {"design", "developing", "review-ready", *INTEGRATED}


def timestamp(value):
    result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None:
        raise ValueError("timestamps require a timezone")
    return result


def evaluate(snapshot: dict, *, now: datetime) -> dict:
    if snapshot.get("schema_version") != "agent-loop.rolling_delivery.v1":
        raise ValueError("unsupported rolling plan schema")
    if not timedelta(0) <= now - timestamp(snapshot["observed_at"]) <= timedelta(minutes=5):
        raise ValueError("snapshot is stale or future dated; reread ledger and GitHub")
    policy = snapshot.get("policy", {})
    limit = policy.get("wip_limit", 5)
    if type(limit) is not int or limit < 1 or (limit != 5 and not policy.get("user_decision_ref")):
        raise ValueError("WIP override requires the recorded user decision")
    decision = snapshot["decision"]
    if type(decision.get("revision")) is not int or decision["revision"] < 1 or decision.get("action") not in {"hold", "proceed"} or not decision.get("evidence_ref"):
        raise ValueError("missing current versioned decision")
    if not snapshot.get("acceptance_owner") or not snapshot.get("batch_id"):
        raise ValueError("batch and common acceptance owner must be named at admission")
    members = snapshot["members"]
    indexed = {member["id"]: member for member in members}
    if len(indexed) != len(members):
        raise ValueError("duplicate member")
    visited, visiting = set(), set()
    def visit(identifier):
        if identifier in visiting:
            raise ValueError("dependency cycle")
        if identifier in visited:
            return
        visiting.add(identifier)
        member = indexed[identifier]
        if member.get("state") not in STATES:
            raise ValueError("unknown delivery state")
        for dependency in member.get("dependencies", []):
            if dependency not in indexed:
                raise ValueError("unknown dependency; include its verified disposition")
            visit(dependency)
        visiting.remove(identifier)
        visited.add(identifier)
    for identifier in indexed:
        visit(identifier)
    eligible, waiting, reconcile = [], {}, []
    for member in members:
        reasons = []
        if member.get("state") != "review-ready":
            reasons.append("not-review-ready")
        if decision["action"] != "proceed":
            reasons.append("current-hold")
        if member.get("adopted_decision_revision") != decision["revision"]:
            reasons.append("decision-not-adopted")
        head = member.get("head_sha", "")
        if not re.fullmatch(r"[0-9a-f]{40}", head) or member.get("ci_head") != head or member.get("reviewed_head") != head or member.get("ci_green") is not True:
            reasons.append("exact-head-gates")
        if any(indexed[dep]["state"] not in INTEGRATED for dep in member.get("dependencies", [])):
            reasons.append("merge-dependency")
        if member.get("merge_blockers"):
            reasons.append("merge-blocked")
        # Shared paths serialize merge execution, not investigation or review.
        if not reasons and any(set(member.get("exclusive_paths", [])) & set(indexed[other].get("exclusive_paths", [])) for other in eligible):
            reasons.append("shared-path-predecessor")
        if reasons:
            waiting[member["id"]] = reasons
        else:
            eligible.append(member["id"])
        if member.get("task_done") and member.get("reservation_active"):
            reconcile.append({"id": member["id"], "action": "verify-external-operations-and-close-reservation-by-owner"})
    cutoff = now >= timestamp(snapshot["cutoff_at"])
    frozen = []
    for member in members:
        if member["state"] in INTEGRATED and all(indexed[dep]["state"] in INTEGRATED for dep in member.get("dependencies", [])):
            frozen.append(member["id"])
    # Freeze a dependency-closed subset. Never wait for all admitted work after cutoff.
    changed = True
    while changed:
        prior = list(frozen)
        frozen = [identifier for identifier in frozen if all(dep in frozen for dep in indexed[identifier].get("dependencies", []))]
        changed = prior != frozen
    tasks = snapshot.get("tasks", [])
    live = {t["id"] for t in tasks if not t.get("terminal") or t.get("claims") or t.get("external_operation")}
    unbound = sum(1 for r in snapshot.get("reservations", []) if r.get("active") and not r.get("task"))
    return {"schema_version": "agent-loop.rolling_delivery_result.v1", "advisory": True,
            "decision_revision": decision["revision"], "merge_next": eligible, "waiting": waiting,
            "freeze_members": frozen if cutoff or len(frozen) == len(members) else [],
            "deferred": [m["id"] for m in members if m["id"] not in frozen] if cutoff else [],
            "acceptance_owner": snapshot["acceptance_owner"], "reconcile": reconcile,
            "wip": {"count": len(live) + unbound, "limit": limit, "new_dispatch_capacity": max(0, limit-len(live)-unbound)},
            "release_gate": "exact-combined-CI-and-frozen-candidate-required; this plan grants no deployment authority"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("snapshot", type=Path)
    args = parser.parse_args()
    try:
        result = evaluate(json.loads(args.snapshot.read_text()), now=datetime.now(timezone.utc))
    except (KeyError, TypeError, ValueError) as error:
        print(json.dumps({"status": "refused", "reason": str(error)}))
        return 1
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
