# SuperCool CLI

Your [SuperCool](https://supercool.com) agent in the terminal. Ask for a video,
a website, a deck, research or images from any shell, script, CI job or coding
agent, and get the finished files on disk. Same agent as on your calls, texts
and the web app; it remembers you everywhere.

```bash
npm i -g @famous-labs/supercool-cli
supercool login
supercool ask "a 15s vertical ad for my candle shop, warm and cozy" --wait
# ✓ Finished: Candle shop ad
# ✓ Saved supercool/candle-shop-ad/ad.mp4
```

## Install

| | |
|---|---|
| npm (macOS, Linux, Windows) | `npm i -g @famous-labs/supercool-cli` |
| Homebrew (macOS) | `brew install famous-labs/tap/supercool` |
| Script (macOS, Linux) | `curl -fsSL https://supercool.com/install.sh \| sh` |
| Manual | download an archive from [Releases](https://github.com/Famous-Labs/supercool-cli/releases) and put `supercool` on your PATH |

## Sign in

```bash
supercool login               # opens your browser once
supercool login --no-browser  # SSH / headless: sign in on any device, paste back the code
```

For CI and scripts, create a personal access token in the dashboard
(`supercool token` opens the page) and set `SUPERCOOL_TOKEN`:

```bash
SUPERCOOL_TOKEN=sc_pat_… supercool ask "…" --wait --json
```

## Commands

| Command | |
|---|---|
| `supercool ask "<message>" [--file PATH]… [--wait] [--out DIR]` | message your agent; `--wait` follows the work it started and saves the files |
| `supercool wait [request-id]` | rejoin work started earlier (after a timeout, a lost connection, or from another machine) |
| `supercool work list` | recent work |
| `supercool work get <id> [--download]` | a chat's status, latest result and files |
| `supercool work open <id>` | open the chat in your browser |
| `supercool whoami` | account, plan, credits |
| `supercool token` | manage personal access tokens (opens the dashboard) |
| `supercool setup [claude\|cursor\|codex]` | install the skills into your coding agent (`--mcp` adds the MCP server too) |
| `supercool login` / `logout` / `update` / `version` | |

Anything else — "stop that", "make it shorter", "how's it going?", "send me the
logo from last week" — is `supercool ask`, exactly as you'd text your agent.

Files: up to 500 MB each with `--file`. Results are saved under
`./supercool/<work-title>/` (or `--out DIR`; `--out -` streams one file to stdout).
Nothing is overwritten, and rerunning never makes duplicate copies.

Global flags: `--json`, `--quiet`, `--no-color`, `--token`, `--profile`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | done; every file saved |
| 1 | other error |
| 2 | sign-in needed |
| 3 | out of credits |
| 4 | `--timeout` reached; the work keeps going (`supercool wait <id>`) |
| 5 | failed |
| 6 | stopped |
| 7 | rate limited, or your agent was busy |
| 8 | finished, but a file didn't save (`supercool wait <id>` retries) |
| 9 | ran past the server's watch window (`supercool wait <id>` recovers it, never resends) |
| 10 | recovery found no outcome; open the chat |

When several pieces of work end differently, the most severe code wins.

## Coding agents

```bash
supercool setup claude
```

or `npx skills add Famous-Labs/supercool-skills`, or in Claude Code
`/plugin marketplace add Famous-Labs/supercool-skills`. See
[supercool-skills](https://github.com/Famous-Labs/supercool-skills).

## Where things are kept

- Login: your OS keychain (falls back to `~/.config/supercool/credentials.json`, mode 0600).
- Local journal of requests and downloads: `~/.config/supercool/state/`.
- Override the folder with `SUPERCOOL_CONFIG_DIR`; skip the daily update check with
  `SUPERCOOL_NO_UPDATE_CHECK=1`.

The CLI sends no telemetry.

## Billing

Work uses your SuperCool credits, like any chat. Nothing extra.

## License

MIT
