# Valinor

Valinor is a AI assistant that can be used to automate tasks and answer questions

```bash
curl -fsSL https://raw.githubusercontent.com/bpicori/valinor/main/scripts/install.sh | bash
```
## Instances

Each instance has its own config, sessions, workspace, memory, cron jobs, gateway port, and Telegram bot.

```bash
# Default instance: ~/.valinor
valinor init
valinor run

# Named instance: ~/.valinor/instances/family
valinor init --instance family
valinor run --instance family

valinor instances
```

`valinor run --instance family` starts the gateway and Telegram bot together when Telegram is configured. A gateway-only instance can set `telegram.enabled = false`.

### systemd user service

```bash
valinor service install --instance family
valinor service status --instance family
valinor service logs --instance family
valinor service uninstall --instance family
```

Instances share `~/.local/bin/valinor` and the Podman image storage, but run in separate processes with separate data. The service unit is `valinor@<instance>.service`.
