#!/usr/bin/env python3
"""Edits the dapr chart (1.18.3) cannot be told to make, re-applied after every
`helm template` by generate-manifests.sh. Text-level and idempotent.

The scheduler's template hard-codes three replicas AND the three-member etcd
peer list (charts/dapr_scheduler/templates/dapr_scheduler_statefulset.yaml:13
and :147); `global.ha.enabled` reaches neither. The platform runs one copy of
every component (TestNoPackageWorkloadShipsMoreThanOneReplica), and on
2026-10-16 the replica count alone was hand-edited to 1. That left an embedded
etcd waiting for two peers that would never exist: no leader for 35 hours
(etcd_server_has_leader{job="scheduler"} = 0, "starting a new election at term
1" in a loop) while /healthz stayed green, so the Jobs API, actor reminders and
every Workflow were dead and nothing said so. Both numbers are rewritten here,
together, and the script refuses to finish if any reference to a second or
third member survives anywhere in the file.

The scheduler's data lives on a node-local volume, so its pod has exactly one
legal node: it takes `adhar-node-bound`, like every other node-bound workload
(data/valkey/manifests/node-bound-priority.yaml says why). The chart only has a
global priorityClassName, which would also pin the four stateless services.
"""
import re
import sys

SOURCE = "# Source: dapr/charts/dapr_scheduler/templates/dapr_scheduler_statefulset.yaml\n"
PEER0 = "dapr-scheduler-server-0=https://dapr-scheduler-server-0.dapr-scheduler-server.adhar-system.svc.cluster.local:2380"

REPLICAS_NOTE = (
    "  # One replica: the platform runs one copy of every component by default and\n"
    "  # scales where a cluster asks for HA (enableHAMode), not by shipping three.\n"
    "  # The etcd peer list below names the same single member; the two numbers\n"
    "  # are rewritten together by post-render.py and must never disagree.\n"
)
PEERS_NOTE = (
    "        # One member, matching `replicas` (post-render.py). A data directory that\n"
    "        # already recorded three members ignores this flag and never elects: wipe\n"
    "        # the scheduler's volume (README.md, \"Recovering a leaderless scheduler\").\n"
)
PRIORITY = (
    "      # Node-local volume: this pod has one legal node (post-render.py).\n"
    "      priorityClassName: adhar-node-bound\n"
)
PEER_LIST = re.compile(r'"(dapr-scheduler-server-0=https://[^",]+)(?:,dapr-scheduler-server-\d+=https://[^",]+)*"')


def fail(msg):
    sys.exit(f"post-render: {msg}; the dapr chart changed shape — re-check charts/dapr_scheduler")


def main(path):
    s = open(path).read()
    start = s.find(SOURCE)
    if start < 0:
        fail("no scheduler StatefulSet in " + path)
    end = s.find("\n---", start + len(SOURCE))
    end = len(s) if end < 0 else end
    sts = s[start:end]

    # replicas: 3 -> 1 (with the note), unless a previous run already did it.
    if "\nspec:\n  replicas: 3\n" in sts:
        sts = sts.replace("\nspec:\n  replicas: 3\n", "\nspec:\n" + REPLICAS_NOTE + "  replicas: 1\n", 1)
    elif REPLICAS_NOTE + "  replicas: 1\n" not in sts:
        fail("the scheduler's replica count is neither the chart's 3 nor this script's 1")

    # --etcd-initial-cluster: keep member 0 only.
    peers = PEER_LIST.search(sts)
    if not peers or peers.group(1) != PEER0:
        fail("no --etcd-initial-cluster value naming dapr-scheduler-server-0")
    sts = sts[: peers.start()] + '"' + PEER0 + '"' + sts[peers.end():]
    flag = '        - "--etcd-initial-cluster"\n'
    if flag not in sts:
        fail("no --etcd-initial-cluster flag")
    if PEERS_NOTE + flag not in sts:
        sts = sts.replace(flag, PEERS_NOTE + flag, 1)

    anchor = "      serviceAccountName: dapr-scheduler\n"
    if anchor not in sts:
        fail("no pod spec with serviceAccountName: dapr-scheduler")
    if PRIORITY not in sts:
        sts = sts.replace(anchor, PRIORITY + anchor, 1)

    s = s[:start] + sts + s[end:]
    for stale in ("dapr-scheduler-server-1", "dapr-scheduler-server-2", "replicas: 3"):
        if stale in s:
            fail(f"{stale!r} survived the rewrite")
    open(path, "w").write(s)
    print("  install.yaml: scheduler is a single-member etcd (replicas 1, one peer), adhar-node-bound")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
