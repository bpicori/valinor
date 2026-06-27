# Debian deployment

Run Valinor as a persistent user service on Debian with automatic GitHub release updates.

## Prerequisites

- `~/.valinor/config.toml` exists (run `valinor init` once, or copy from another machine)
- `curl`, `jq`, `podman`, and `systemd` user session
- For private repos: copy `valinor.env.example` to `~/.config/valinor/valinor.env` and set `GITHUB_TOKEN`

## One-time setup

Clone the repo on the Debian laptop, then:

```bash
bash deploy/debian/install.sh
```

This will:

1. Install `~/.local/bin/valinor` from the latest GitHub release
2. Enable `valinor.service` (`-mode=all`: gateway + Telegram)
3. Enable a 30-minute update timer
4. Enable systemd linger so the service survives logout/reboot

## Manual update

```bash
valinor-update
```

## Useful commands

```bash
systemctl --user status valinor
journalctl --user -u valinor -f
systemctl --user status valinor-update.timer
journalctl --user -u valinor-update -f
valinor -version
```

## Rollback

If an update misbehaves, restore the previous binary:

```bash
cp ~/.local/bin/valinor.prev ~/.local/bin/valinor
systemctl --user restart valinor
```

## Notes

- First start after a version bump may rebuild the Podman sandbox image (can take several minutes).
- Logs: `~/.valinor/logs/valinor.log` and `~/.valinor/logs/deploy.log`
- Update check logs: `journalctl --user -u valinor-update`