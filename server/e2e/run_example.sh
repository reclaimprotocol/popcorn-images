#!/usr/bin/env bash
#
# All-in-one: drive the embedded "example" provider config against a running
# popcorn image — start a session, stream events live, print proofs on exit.
# No external config file needed.
#
# Usage:
#   BASE_URL=http://127.0.0.1:444 ./run_example.sh           # use embedded config
#   BASE_URL=http://127.0.0.1:444 ./run_example.sh my.json   # use a config file instead
#   CONFIG_URL=https://.../providers/example ./run_example.sh # fetch config first
#
# Env: BASE_URL (default :444), EVENTS_SECONDS (default 300). Requires curl + jq.
set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
RUNNER="$SCRIPT_DIR/run_provider_config.sh"
[[ -x "$RUNNER" ]] || { echo "error: $RUNNER not found/executable"; exit 1; }

# If a config file is passed, just delegate to the generic runner.
if [[ $# -ge 1 && -n "${1:-}" ]]; then
  exec "$RUNNER" "$1"
fi

command -v jq >/dev/null 2>&1 || { echo "error: jq is required"; exit 1; }

TMP=$(mktemp "${TMPDIR:-/tmp}/provider-config.XXXXXX.json")
trap 'rm -f "$TMP"' EXIT

if [[ -n "${CONFIG_URL:-}" ]]; then
  echo "==> fetching provider config from $CONFIG_URL"
  curl -fsS "$CONFIG_URL" -o "$TMP" || { echo "error: fetch failed"; exit 1; }
else
  echo "==> using embedded 'example' provider config"
  cat > "$TMP" <<'PROVIDER_EOF'
{
  "providerId": "example",
  "loginUrl": "https://example.org/",
  "customInjection": "\"use strict\";\n(() => {\n  // src/utils.ts\n  function notes(_documentation) {\n  }\n\n  // src/providers/example.js\n  var onReady = async () => {\n    if (window.Reclaim) {\n      window.Reclaim.updatePublicData({\n        data: [\n          {\n            userId: 1,\n            id: 1,\n            title: \"sunt aut facere repellat provident occaecati excepturi optio reprehenderit\",\n            body: \"quia et suscipit\\nsuscipit recusandae consequuntur expedita et cum\\nreprehenderit molestiae ut ut quas totam\\nnostrum rerum est autem sunt rem eveniet architecto\"\n          },\n          {\n            userId: 1,\n            id: 2,\n            title: \"qui est esse\",\n            body: \"est rerum tempore vitae\\nsequi sint nihil reprehenderit dolor beatae ea dolores neque\\nfugiat blanditiis voluptate porro vel nihil molestiae ut reiciendis\\nqui aperiam non debitis possimus qui neque nisi nulla\"\n          },\n          {\n            userId: 1,\n            id: 3,\n            title: \"ea molestias quasi exercitationem repellat qui ipsa sit aut\",\n            body: \"et iusto sed quo iure\\nvoluptatem occaecati omnis eligendi aut ad\\nvoluptatem doloribus vel accusantium quis pariatur\\nmolestiae porro eius odio et labore et velit aut\"\n          }\n        ]\n      });\n    }\n\n    const currentUrl = window.location.href;\n\n    if (currentUrl === 'https://example.org/') {\n      setTimeout(() => {\n        window.location.href = 'https://example.com/';\n      }, 2000);\n    }\n    else if (currentUrl === 'https://example.com/') {\n      setTimeout(() => {\n        window.location.href = 'https://jsonplaceholder.typicode.com/users/1';\n      }, 2000);\n    }\n  };\n  if (document.readyState === 'complete') {\n    onReady().catch(console.error);\n  } else {\n    window.addEventListener(\"load\", (_) => {\n      notes(\"page is fully loaded\");\n      setTimeout(async () => {\n        onReady().catch(console.error);\n      }, 1e3);\n    });\n  }\n})();\n",
  "userAgent": { "ios": "", "android": "" },
  "geoLocation": "",
  "injectionType": "HAWKEYE",
  "disableRequestReplay": false,
  "verificationType": "WITNESS",
  "requestData": [
    {
      "url": "https://example.org/",
      "expectedPageUrl": "https://example.org",
      "urlType": "TEMPLATE",
      "method": "GET",
      "responseMatches": [
        { "value": "{{pageTitle}}", "type": "contains", "invert": false, "description": null, "order": 0, "isOptional": false },
        { "value": "<a href={{ianaLinkUrl}}>Learn more</a>", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false }
      ],
      "responseRedactions": [
        { "xPath": "//title/text()", "jsonPath": "", "regex": "(.*)", "hash": "", "order": null },
        { "xPath": "/html/body/div[1]/p[2]/a", "jsonPath": "", "regex": "<a href=(.*)>Learn more</a>", "hash": null, "order": null }
      ],
      "bodySniff": { "enabled": false, "template": "" },
      "responseVariables": ["pageTitle", "ianaLinkUrl"],
      "writeRedactionMode": "zk",
      "credentials": null
    },
    {
      "url": "https://example.com/",
      "expectedPageUrl": "https://example.com",
      "urlType": "TEMPLATE",
      "method": "GET",
      "responseMatches": [
        { "value": "{{pageTitle}}", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false },
        { "value": "<a href={{ianaLinkUrl}}>Learn more</a>", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false }
      ],
      "responseRedactions": [
        { "xPath": "//title/text()", "jsonPath": "", "regex": "(.*)", "hash": null, "order": null },
        { "xPath": "/html/body/div[1]/p[2]/a", "jsonPath": "", "regex": "<a href=(.*)>Learn more</a>", "hash": null, "order": null }
      ],
      "bodySniff": { "enabled": false, "template": "" },
      "responseVariables": ["pageTitle", "ianaLinkUrl"],
      "writeRedactionMode": "zk",
      "credentials": null
    },
    {
      "url": "https://jsonplaceholder.typicode.com/users/1",
      "expectedPageUrl": "https://jsonplaceholder.typicode.com/users/1",
      "urlType": "TEMPLATE",
      "method": "GET",
      "responseMatches": [
        { "value": "{{FullName}}", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false },
        { "value": "{{UserName}}", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false },
        { "value": "{{Email}}", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false },
        { "value": "{{id}}", "type": "contains", "invert": false, "description": "", "order": null, "isOptional": false }
      ],
      "responseRedactions": [
        { "xPath": "", "jsonPath": "$.name", "regex": "\"name\": \"(.*)\"", "hash": null, "order": null },
        { "xPath": "", "jsonPath": "$.username", "regex": "\"username\": \"(?<username>.*)\"", "hash": null, "order": null },
        { "xPath": "", "jsonPath": "$.email", "regex": "\"email\": \"(?<email>.*)\"", "hash": "oprf", "order": null },
        { "xPath": "", "jsonPath": "$.id", "regex": "\"id\": (.*)", "hash": null, "order": 0 }
      ],
      "bodySniff": { "enabled": false, "template": "" },
      "responseVariables": ["FullName", "UserName", "Email", "id"],
      "writeRedactionMode": "zk",
      "credentials": null
    }
  ],
  "allowedInjectedRequestData": [],
  "pageTitle": "",
  "metadata": {},
  "stepsToFollow": null,
  "useIncognitoWebview": null,
  "extensionConfig": null
}
PROVIDER_EOF
fi

jq -e . "$TMP" >/dev/null 2>&1 || { echo "error: config is not valid JSON"; exit 1; }
"$RUNNER" "$TMP"
