#!/bin/sh
# One time only, before the first release (plan: npm bootstrap).
#
# npm trusted publishing is configured per package, and a package must
# already exist to configure it. So publish a 0.0.1 placeholder of the
# wrapper and of every platform package with a short-lived granular token,
# then, on npmjs.com, set each package's trusted publisher to
# Famous-Labs/supercool-cli + .github/workflows/release.yml, and revoke the
# token. Every release after that is tokenless.
#
#   NPM_TOKEN=npm_… sh scripts/npm-bootstrap.sh
set -eu
[ -n "${NPM_TOKEN:-}" ] || { echo "set NPM_TOKEN to a short-lived granular token" >&2; exit 1; }
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
printf '//registry.npmjs.org/:_authToken=%s\n' "$NPM_TOKEN" > "$TMP/.npmrc"
for name in supercool-cli supercool-cli-darwin-arm64 supercool-cli-darwin-x64 supercool-cli-linux-arm64 \
            supercool-cli-linux-x64 supercool-cli-win32-arm64 supercool-cli-win32-x64; do
  d="$TMP/$name"; mkdir -p "$d"
  printf '{"name":"@famous-labs/%s","version":"0.0.1","description":"Placeholder: the SuperCool CLI is published from GitHub Actions.","license":"MIT","repository":{"type":"git","url":"git+https://github.com/Famous-Labs/supercool-cli.git"}}\n' "$name" > "$d/package.json"
  printf 'Placeholder. Install @famous-labs/supercool-cli.\n' > "$d/README.md"
  (cd "$d" && npm publish --access public --userconfig "$TMP/.npmrc")
done
echo "Now configure trusted publishing for each of the 7 packages on npmjs.com, then revoke the token."
