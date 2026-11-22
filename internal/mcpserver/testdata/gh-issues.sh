#!/usr/bin/env bash
# Stand-in for gh. Only the login and API calls used by this fixture exist.
set -euo pipefail
case "$*" in
    --version) echo "gh version 2.0.0"; exit ;;
    'auth login --web --hostname github.com')
        echo '{"id":42,"login":"fixture-user"}' > "$GH_FIXTURE/actor"
        exit ;;
esac
[ "$1 $2 $3 $4" = "api --hostname github.com --method" ]
[ "$GH_PROMPT_DISABLED" = 1 ]
method=$5
while [ "$1" != "--" ]; do shift; done
[ "$#" = 2 ]
case "$method $2" in
    'GET user')
        [ -f "$GH_FIXTURE/actor" ] || exit 4
        printf 'HTTP/2.0 200 OK\r\n\r\n'
        /bin/cat "$GH_FIXTURE/actor" ;;
    'GET repos/sample/project/issues?'*)
        printf 'HTTP/2.0 200 OK\r\n\r\n[]' ;;
    'POST repos/sample/project/issues')
        /bin/cat >> "$GH_FIXTURE/posts"
        printf '\n' >> "$GH_FIXTURE/posts"
        printf 'HTTP/2.0 201 Created\r\n\r\n{"number":17,"title":"Example","state":"open","html_url":"https://github.com/sample/project/issues/17"}' ;;
    *) exit 1 ;;
esac
