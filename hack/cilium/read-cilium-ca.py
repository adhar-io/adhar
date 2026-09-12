#!/usr/bin/env python3
"""Print the base64 cilium-ca cert or key out of the embedded Cilium install
manifest, so the Cluster Mesh render can be pinned to the same CA."""
import re
import sys

manifest, which = sys.argv[1], sys.argv[2]
text = open(manifest).read()
start = text.find("name: cilium-ca")
if start < 0:
    sys.exit("cilium-ca secret not found in " + manifest)
block = text[start:start + 6000]
match = re.search(r"ca\.%s:\s*(\S+)" % which, block)
if not match:
    sys.exit("ca.%s not found in the cilium-ca secret" % which)
print(match.group(1))
