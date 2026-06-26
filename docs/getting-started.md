# Getting started

Step-by-step setup for a Synology NAS operator.

## Prerequisites

- Docker Engine running on the NAS (Container Manager or standalone Docker)
- Access to your UniFi Network controller (self-hosted or Cloud Key)
- An account with Super Admin or Network Admin rights to create additional admin accounts

## 1. Create a bollard service account in UniFi

UniFi has no DNS-only role. bollard needs a local Network Admin account.

1. Open UniFi Network → **Settings** → **Admins & Users**
2. Click **Add Admin**
3. Set **Account Type** to **Local Access Only** (not Ubiquiti SSO)
4. Username: `bollard` (or any name you prefer)
5. Role: **Network Admin**
6. Save the account
7. Open the new account's profile → **API Keys** → **Generate API Key**
8. Copy the key — it is only shown once

> Do not use your primary admin account or an SSO account. A dedicated local account limits blast radius if the key is compromised.

## 2. Create a `.env` file

Store credentials outside your compose file so they are not committed to version control.

```bash
# /volume1/docker/bollard/.env
UNIFI_HOST=https://192.168.1.1        # or https://unifi.home.arpa
UNIFI_API_KEY=your-api-key-here
DATABASE_URL=file:/data/bollard.db
```

Optional overrides:

```bash
UNIFI_SITE=default                    # change if you use a named site
UNIFI_SKIP_TLS_VERIFY=true            # set false if you have a trusted cert
RECONCILE_INTERVAL=5m
LOG_LEVEL=info
```

Restrict permissions:

```bash
chmod 600 /volume1/docker/bollard/.env
```

## 3. Add bollard to docker-compose.yml

```yaml
# /volume1/docker/bollard/docker-compose.yml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    env_file: .env

volumes:
  bollard-data:
```

Start bollard:

```bash
cd /volume1/docker/bollard
docker compose up -d
```

## 4. Label your first container

Add a `dns.bollard/hostname` label to any container you want registered:

```yaml
services:
  myapp:
    image: nginx:alpine
    labels:
      dns.bollard/hostname: myapp.home.arpa
```

Restart the container to trigger the start event:

```bash
docker compose up -d
```

## 5. Verify the DNS record was created

**In the UniFi UI:**

1. UniFi Network → **Settings** → **DNS**
2. Look for an A record with the hostname you set
3. The record should point to the NAS IP address

**From the CLI:**

```bash
# Query your UniFi controller as DNS resolver
dig myapp.home.arpa @192.168.1.1

# Or use the domain if your clients already use UniFi DNS
dig myapp.home.arpa
```

Expected output includes an `ANSWER SECTION` with an `A` record.

**Check bollard logs:**

```bash
docker compose logs bollard
```

Healthy output looks like:

```
level=info msg="bollard starting" version=v0.2.1
level=info msg="connected to docker" server_version=24.0.5
level=info msg="connected to unifi" host=https://unifi.home.arpa
level=info msg="dns record created" hostname=myapp.home.arpa ip=192.168.1.100 record_id=abc123
```

## 6. Troubleshoot first-run issues

Enable debug logging for more detail:

```bash
# In .env
LOG_LEVEL=debug
```

```bash
docker compose up -d && docker compose logs -f bollard
```

**Common issues:**

| Symptom | Likely cause | Fix |
|---|---|---|
| `connection refused` to UniFi | Wrong `UNIFI_HOST` or controller not reachable | Check host URL and network connectivity |
| `401 Unauthorized` | Bad `UNIFI_API_KEY` | Regenerate key in UniFi, update `.env` |
| `certificate verify failed` | Self-signed UniFi cert | Set `UNIFI_SKIP_TLS_VERIFY=true` or mount CA cert |
| No record created, no error | Container missing `dns.bollard/hostname` label | Verify label is set, restart container |
| `fatal: cannot open database` | `DATABASE_URL` path not writable | Check volume mount and permissions |
| Docker socket permission denied | Synology DSM user not in `docker` group | Run bollard as root or adjust socket permissions |
