# Deployment

Run Valinor as a persistent user service on Linux (systemd) or macOS (launchd) with automatic GitHub release updates.

## Prerequisites

- `~/.valinor/config.toml` exists (run `valinor init` once, or copy from another machine)
- `podman` installed and on `PATH`
- Linux: systemd user session
- macOS: launchd (default on macOS)

## One-time setup

```bash
valinor init
valinor install
```

This will:

1. Install `~/.local/bin/valinor` from the latest GitHub release
2. Enable the Valinor service (`run --mode=all`: gateway + Telegram)
3. Enable a 30-minute update timer
4. On Linux: enable systemd linger so the service survives logout/reboot

## Operator commands

```bash
valinor status
valinor logs -f
valinor logs --deploy
valinor update
valinor restart
valinor rollback
valinor uninstall
valinor version
```

## Logs

| Platform | `valinor logs` | `valinor logs --deploy` |
|----------|----------------|-------------------------|
| Linux | `journalctl --user -u valinor` | `~/.valinor/logs/deploy.log` |
| macOS | `~/.valinor/logs/valinor.log` | `~/.valinor/logs/deploy.log` |

Application logs are also written to `~/.valinor/logs/valinor.log` on both platforms.

## Rollback

If an update misbehaves:

```bash
valinor rollback
```

This restores `~/.local/bin/valinor.prev` and restarts the service.

## Notes

- First start after a version bump may rebuild the Podman sandbox image (can take several minutes).
- `valinor uninstall` removes the service but keeps `~/.valinor` data and configuration.
- Re-run `valinor install` after upgrading valinor to refresh systemd/launchd unit files.