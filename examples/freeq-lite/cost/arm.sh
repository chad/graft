#!/bin/bash
# arm.sh <name> <model> <samples> <run#> [extra graft flags]
D=$(cd "$(dirname "$0")" && pwd); n=$1 m=$2 s=$3 r=$4; shift 4
W=$D/runs/$n-$r; rm -rf $W; mkdir -p $W/app $W/repo; export COSTFILE=$W/cost
cd $D/fl; t0=$(date +%s)
$D/graft run --config g-$m.json --spec spec/freeq-lite.md --repo $W/repo --root $W/app --accept --samples $s "$@" > $W/log 2>&1
rc=$?   # 0: every region converged and was applied; anything else: it did not
t1=$(date +%s)
(cd $W/app && go build -o ../server . 2>$W/build.err) && $D/oracle -bin $W/server > $W/oracle.json 2>&1
echo "$n $r graft_exit=$rc wall=$((t1-t0))s cost=$(paste -sd+ $W/cost | bc) calls=$(wc -l < $W/cost) oracle=$(cat $W/oracle.json | head -c 200)" >> $D/results.txt
