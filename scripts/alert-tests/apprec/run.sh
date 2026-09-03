#!/usr/bin/env bash
# Fire every apprec alert against synthetic series with `promtool test rules`.
#
# The VMRule (kubernetes/apps/observability/victoria-metrics/app/vmrule-apprec.yaml)
# is the deployed artefact; promtool wants a plain Prometheus rules file, so
# spec.groups is extracted verbatim into a temp file first. VictoriaMetrics'
# vmalert evaluates the same PromQL (the VMRule-only fields used here --
# keep_firing_for -- are Prometheus-compatible), so a promtool pass is a
# faithful proof that the expressions fire on the conditions described.
#
#   scripts/alert-tests/apprec/run.sh            # needs docker (prom/prometheus)
#   PROMTOOL=promtool scripts/alert-tests/apprec/run.sh
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
VMRULE="$ROOT/kubernetes/apps/observability/victoria-metrics/app/vmrule-apprec.yaml"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

/usr/bin/env python3 - "$VMRULE" "$WORK/apprec.rules.yaml" <<'EOF'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
groups = doc["spec"]["groups"]
# The tests pin expressions, labels and timing; annotations are prose and
# promtool would otherwise demand every summary/description be repeated here.
for group in groups:
    for rule in group["rules"]:
        rule.pop("annotations", None)
yaml.safe_dump({"groups": groups}, open(sys.argv[2], "w"), sort_keys=False, width=1000)
EOF
cp "$HERE/tests.yaml" "$WORK/tests.yaml"

if [ -n "${PROMTOOL:-}" ]; then
  (cd "$WORK" && "$PROMTOOL" check rules apprec.rules.yaml && "$PROMTOOL" test rules tests.yaml)
else
  docker run --rm --user "$(id -u):$(id -g)" -v "$WORK:/w:z" -w /w --entrypoint promtool prom/prometheus:v2.53.4 check rules apprec.rules.yaml
  docker run --rm --user "$(id -u):$(id -g)" -v "$WORK:/w:z" -w /w --entrypoint promtool prom/prometheus:v2.53.4 test rules tests.yaml
fi
