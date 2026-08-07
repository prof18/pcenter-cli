# pcenter documentation

| Document | What it covers |
| --- | --- |
| [COMMANDS.md](COMMANDS.md) | Every command, flag by flag, and the behavior behind them. |
| [METADATA.md](METADATA.md) | The metadata directory format (`store.json`, `listings/`, `images-manifest.json`) and the release-notes file contract. |
| [AUTOMATION.md](AUTOMATION.md) | The machine contract: JSON shapes, error codes, exit codes, and rules for agents driving the CLI. |
| [CI.md](CI.md) | Installing pcenter on a runner, credentials from secrets, and the release and listing workflows. |
| [TROUBLESHOOTING.md](TROUBLESHOOTING.md) | Failures you are likely to hit, and what to do about each. |

`pcenter <command> --help` is always current with the binary you have; these pages add the reasoning the help text has no room for.

Design notes and implementation history live in [`../plan/`](../plan/INDEX.md) — useful if you want to know *why* something works the way it does, not needed to use the tool.
