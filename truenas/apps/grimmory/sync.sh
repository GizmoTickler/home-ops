#!/bin/sh
# Refresh Grimmory library 1 every 15 minutes (was the k8s CronJob grimmory-library-sync).
# Credentials come from the container env (1Password refs in compose.yaml).
while true; do
    sleep 900
    body=$(printf '{"username":"%s","password":"%s"}' "$GRIMMORY_SYNC_USERNAME" "$GRIMMORY_SYNC_PASSWORD")
    token=$(curl -fsS -H 'Content-Type: application/json' -d "$body" http://grimmory:6060/api/v1/auth/login \
        | sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p')
    if [ -n "$token" ] && curl -fsS -X PUT -H "Authorization: Bearer $token" \
        http://grimmory:6060/api/v1/libraries/1/refresh >/dev/null; then
        echo "refreshed library 1"
    else
        echo "library refresh failed"
    fi
done
