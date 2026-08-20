# CommandCode Bridge

CommandCode Bridge is an unofficial native CLIProxyAPI plugin for using your own Command Code account through OpenAI-compatible APIs.

> **Unofficial project.** CommandCode Bridge is community-maintained and is not affiliated with, not endorsed by, not sponsored by, not authorized by, and not supported by Langbase, Inc. d/b/a Command Code or the CLIProxyAPI project. You need your own authorized Command Code account and credentials. Command Code's [Terms of Service](https://commandcode.ai/terms), [Privacy Policy](https://commandcode.ai/privacy), pricing, quotas, and usage rules apply. See [DISCLAIMER.md](DISCLAIMER.md).

## Prerequisites

- CLIProxyAPI v7.2.133 or a compatible build with native plugin support enabled.
- Go 1.26 for local builds and a working C compiler for `-buildmode=c-shared`.
- A Command Code API key beginning with `user_` for live requests.
- `zip`, `curl`, and either `sha256sum` or `shasum` for packaging and smoke checks.

## Build

```sh
./scripts/build.sh dev ./dist
```

The command builds `dist/commandcode-bridge.dylib` on macOS, `dist/commandcode-bridge.so` on Linux/FreeBSD, or `dist/commandcode-bridge.dll` on Windows. The generated C header is removed.

## Install

Copy the library into CPA's configured plugin directory using the platform filename above. Do not replace a library while CPA has it loaded; stop CPA, replace the file, then restart it.

Example default locations:

```text
~/.cli-proxy-api/plugins/commandcode-bridge.dylib
~/.cli-proxy-api/plugins/commandcode-bridge.so
~/.cli-proxy-api/plugins/commandcode-bridge.dll
```

## Configuration

```yaml
routing:
  strategy: round-robin

plugins:
  enabled: true
  dir: ~/.cli-proxy-api/plugins
  configs:
    commandcode-bridge:
      enabled: true
```

After restart, `/v0/management/plugins` must report `commandcode-bridge` with `registered: true` and `effective_enabled: true`.

### Migration

The old `commandcode` plugin configuration and auth identities are not aliases. Configure and enroll `commandcode-bridge` separately; old plugin identities are not recognized by CommandCode Bridge.

## Enrollment

Open **CommandCode Bridge Accounts** in CPA Management Center.

- **Validate and add:** paste a `user_...` API key and optional label. Select the account plan and, if needed, set a priority override between `1` and `10`. The plugin validates the key through Command Code before asking CPA to save canonical auth JSON.
- **Validate only:** performs the same live validation without persistence.
- **Import local CLI credential:** reads only `~/.commandcode/auth.json`; request data cannot override this path. Select the plan and optional priority override before importing.
- **Edit routing:** open **Routing** on an account card, choose the plan and optional priority override, then save. The page sends CPA's authenticated native Auth Files field update without asking for the account API key.
- **Choose models per account:** open **Models** on an account card. Fetch the live Command Code catalog with that account's key, select models, optionally set a client alias per model, add custom model IDs, and save. An account exposes no models until you select at least one; a request for a model that account does not list is rejected with `400`.
- **Aliases:** an alias is a client-visible name mapped to one upstream Command Code model for that account. A request using the alias is rewritten to the upstream model before execution. Leave the alias empty to call the model by its upstream name.
- **Delete or disable:** use CPA's **Auth Files** page. The plugin intentionally exposes no deletion route.
- **Management access:** the optional disclosure stays collapsed when CPAP provides remembered management credentials. If no credential is available, open it and enter the management password for the current tab.

Stored files use `commandcode-bridge-<12-hex-fingerprint>.json`. Raw keys, auth JSON, paths, auth indexes, and Authorization headers are excluded from plugin responses and logs. Editing an account's models writes the updated model set to the physical credential file; CPA v7.2.133 may rebuild the in-memory auth record as active on that save, so re-apply any disabled/status flag through **Auth Files** if needed.

## Account Routing

| Plan | Priority |
|---|---:|
| Go | 7 |
| GOAT | 6 |
| Pro | 5 |
| Team / Team Pro | 4 |
| Max 10x | 3 |
| Max 20x | 2 |
| Provider | 1 |
| Unspecified | 0 |

CPA selects the highest healthy priority that supports the requested model. With `round-robin`, CPA distributes requests among accounts at that priority and uses lower priorities when necessary. A priority override between `1` and `10` replaces the plan preset; equal values merge accounts into one pool. `fill-first` exhausts capacity in priority order and does not switch accounts every N requests.

Existing unclassified accounts remain at priority `0` until edited. When both `plan` and `priority_override` are absent, a valid legacy top-level `priority` from `1` to `10` is preserved as the override. The plugin does not detect account plans or query billing and quota endpoints.

## Usage

Non-streaming request:

```sh
curl http://127.0.0.1:8317/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_CPA_CLIENT_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"Hello"}]}'
```

Streaming request:

```sh
curl -N http://127.0.0.1:8317/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_CPA_CLIENT_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

Supported input covers text messages, function tools and tool results, reasoning output, `max_tokens`/`max_completion_tokens`, `temperature`, `top_p`, and `stream_options.include_usage`. Unsupported content returns a structured error instead of lossy conversion.

Model discovery first asks Command Code's public catalog through CPA host transport. Failure falls back to the bundled snapshot.

## Security

All upstream HTTP and auth persistence operations cross CPA host callbacks; the plugin has no custom network client. The browser resource is static, same-origin, dependency-free, and read-only on its public GET route. Account list/add/import/validate calls use authenticated `/v0/management/plugins/commandcode-bridge/...` routes. The plugin list exposes only redacted routing state and filename; no auth index, key, raw JSON, or path reaches the browser.

Management Center's remembered password format is reversible browser-side obfuscation, not a cryptographic security boundary. If no remembered password is available, the accounts page accepts a tab-local password input and does not persist it.

Never publish logs, auth files, request bodies, management passwords, or API keys when reporting problems.

## Troubleshooting

- **Plugin not listed:** confirm CPA was built with plugin support, the filename matches the current OS, and `plugins.enabled` plus `plugins.configs.commandcode-bridge.enabled` are true.
- **Plugin listed but ineffective:** inspect CPA startup logs for ABI/schema or shared-library loader errors; stop CPA before replacing the library.
- **Credential rejected:** the key must begin with `user_` and pass a live `ping` validation request.
- **No models:** verify the selected auth file is recognized as provider `commandcode-bridge`; catalog failure should still expose bundled snapshot models.
- **Management page asks for password:** log in through Management Center with “remember password” enabled or enter it for the current tab.

## Releases

Package the current platform:

```sh
./scripts/build.sh 0.1.0 ./dist
./scripts/package.sh 0.1.0 ./dist/commandcode-bridge.dylib ./dist
```

Asset names are `commandcode-bridge_<version>_<goos>_<goarch>.zip`; the archive contains one library at its root and `checksums.txt` records SHA-256 digests. The repository includes an unpublished schema-1 store template with no version because no release exists.

## Verification

```sh
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -race ./...
sh ./scripts_test.sh
./scripts/build.sh dev ./dist
```

For a running CPA instance:

```sh
CPA_MANAGEMENT_KEY='YOUR_MANAGEMENT_KEY' \
CPA_MANAGEMENT_PATH='/v0/management/plugins/commandcode-bridge/accounts' \
CPA_RESOURCE_PATH='/v0/resource/plugins/commandcode-bridge/accounts' \
./scripts/smoke.sh http://127.0.0.1:8317
```

Live Command Code end-to-end verification is optional and requires an authorized `COMMAND_CODE_API_KEY` in the local environment. No live-service success is claimed when that credential is absent.

## License

MIT. See [LICENSE](LICENSE).
