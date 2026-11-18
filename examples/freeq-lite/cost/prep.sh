#!/bin/bash
# prep.sh: set up the M9 measurement dir $M (default ~/m9) from the graft checkout this script lives in.
# Builds graft + oracle, copies freeq-lite WITHOUT oracle/ and reference/ (held out), writes g-sonnet.json / g-haiku.json
# = the repo's post-#21 graft.json with model.argv -> claudecost.sh <model>. Run: ./prep.sh ; then ./arm.sh A sonnet 1 1 ; ./arm.sh B haiku 3 1 ...
set -euo pipefail
R=$(git -C "$(dirname "$0")" rev-parse --show-toplevel); M=${M:-~/m9}; S=$(cd "$(dirname "$0")" && pwd)
mkdir -p $M/runs; cp $S/arm.sh $S/claudecost.sh $M/; chmod +x $M/*.sh
(cd $R && go build -o $M/graft ./cmd/graft && go build -o $M/oracle ./examples/freeq-lite/oracle)
rm -rf $M/fl; rsync -a --exclude oracle --exclude reference $R/examples/freeq-lite/ $M/fl/
for m in sonnet haiku; do jq --arg c "$M/claudecost.sh" --arg m $m '.model.argv=[$c,$m]' $M/fl/graft.json > $M/fl/g-$m.json; done
jq -c '.policy, [.gates[].label]' $M/fl/g-haiku.json
echo "ready: $M"
