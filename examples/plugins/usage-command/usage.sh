#!/bin/sh
# A command plugin prints one JSON object per line. code-speed runs it every
# "every" seconds in this folder, with CODE_SPEED_SINCE_MS (report what ended
# at or after it), CODE_SPEED_STATE_DIR (keep state there) and
# CODE_SPEED_PLUGIN_DIR in its environment. Any language works.
now=$(($(date +%s) * 1000))
echo "{\"type\":\"response\",\"key\":\"demo-$now\",\"startMs\":$((now - 4000)),\"endMs\":$now,\"model\":\"gpt-6.1-terra\",\"project\":\"demo\",\"session\":\"s1\",\"input\":5000,\"cachedInput\":4000,\"output\":300}"
echo "{\"type\":\"quota\",\"plan\":\"pro\",\"windows\":[{\"kind\":\"fiveHour\",\"usedPercent\":42,\"windowMinutes\":300,\"resetsAtMs\":$((now + 3600000))}]}"
echo "{\"type\":\"session\",\"session\":\"s1\",\"project\":\"demo\",\"model\":\"gpt-6.1-terra\",\"phase\":\"idle\",\"context\":5000,\"contextWindow\":272000}"
