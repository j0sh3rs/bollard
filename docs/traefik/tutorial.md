# Tutorial: register a Traefik route in UniFi DNS

You will run Traefik 3.7.19, an nginx backend, and the Bollard plugin with Docker Compose. Traefik routes `app.home.arpa` to nginx. Bollard notices that route and creates `app.home.arpa → <Traefik IP>` in UniFi DNS. You then prove routing and DNS separately.

The Compose project loads Bollard plugin release `v0.8.0`. <!-- x-release-please-version -->

You know Docker and Traefik. You do not need to know Bollard or the UniFi API.

## Before you start

- Docker with Compose v2, plus `curl` and `dig`.
- A UniFi Network controller you can reach from this machine, with its address (for example `https://192.0.2.1`).
- A UniFi API key from a local account allowed to manage DNS policies.
- Outbound access from the Traefik container to the Traefik plugin catalog. Without it, use the [local plugin overlay](how-to.md#develop-with-a-local-plugin) instead.
- A name under a domain you control locally. This tutorial uses `app.home.arpa`. Pick an unused name: Bollard never edits an existing record.

Everything lives in [`docs/examples/traefik-plugin/`](../examples/traefik-plugin/). Work from that directory:

```sh
cd docs/examples/traefik-plugin
```

## 1. Know what you are about to start

| Service | Image | Role |
|---|---|---|
| `render` | `busybox:stable` | One-shot job. Fills the Traefik config templates from your `.env`. |
| `traefik` | `traefik:v3.7.19` | Routes `Host(app.home.arpa)` to nginx and runs the plugin. |
| `app` | `nginx:stable-alpine` | The backend. Serves the default nginx page. |

Traefik's API listens on `127.0.0.1:8081` inside the Traefik container only. Compose publishes just the web entrypoint. The API remains reachable by trusted processes sharing Traefik's network namespace, not by ordinary clients.

## 2. Configure non-secret values

```sh
cp env.example .env
```

Edit `.env`:

| Variable | Set it to |
|---|---|
| `UNIFI_HOST` | Your controller URL. It must be reachable from inside the Traefik container. |
| `UNIFI_SITE` | `Default` for a stock controller. Releases before `v0.7.1` accept only the site display name or UUID. |
| `TRAEFIK_ADDRESS` | The IPv4 address clients will use to reach Traefik. Not the nginx IP, not the controller. |
| `APP_HOST` | `app.home.arpa`, or your chosen name. |

Leave `UNIFI_SKIP_TLS_VERIFY=false`. If your controller uses a private CA, stop here and follow [Verify the controller with a private CA](how-to.md#verify-the-controller-with-a-private-ca), then continue.

Keep `HTTP_BIND=127.0.0.1` and `HTTP_PORT=8000` for now. That publishes Traefik on loopback only.

## 3. Create the API key secret

```sh
umask 077
mkdir -p secrets
cat > secrets/unifi_api_key
```

Paste the key, press Enter, then Ctrl-D. Compose mounts the file at `/run/secrets/unifi_api_key`. The plugin trims surrounding whitespace. `.env` and `secrets/` are git-ignored in this directory.

## 4. Check the Compose project

```sh
docker compose config --quiet && echo valid
```

Compose fails here if `UNIFI_HOST` or `TRAEFIK_ADDRESS` is missing. It does not contact UniFi.

## 5. Start the stack

```sh
docker compose up -d
docker compose logs render
docker compose logs traefik
```

Expected: `render` prints `render: wrote /rendered/traefik.yml ...` and exits with status 0. Traefik stays running. If `render` reports a forbidden character, remove `|`, `&`, `\`, `"` and backticks from your `.env` values.

If Traefik exits while loading the plugin, inspect its logs for download, manifest, interpreter, or configuration errors. See [Troubleshoot](how-to.md#troubleshoot).

## 6. Watch Bollard create the record

The plugin polls immediately and every 30 seconds. Allow up to one minute.

```sh
docker compose logs -f traefik
```

Look for a log line containing the following text; Traefik also adds the provider name and a timestamp:

```text
created A record app.home.arpa -> 192.0.2.10
```

Stop following with Ctrl-C. The record has TTL 300.

Silence is not success. Bollard contacts UniFi only once Traefik reports at least one eligible router, and errors repeat on every poll. If you see an error instead, read it against the [troubleshooting table](how-to.md#troubleshoot).

## 7. Prove routing, with DNS out of the picture

`curl --resolve` pins the name to a local address, so this test passes or fails purely on Traefik routing:

```sh
set -a; . ./.env; set +a
curl -i --resolve "$APP_HOST:$HTTP_PORT:127.0.0.1" "http://$APP_HOST:$HTTP_PORT/"
```

Expected: `HTTP/1.1 200 OK`, `Server: nginx`, and the nginx welcome page. A `404 page not found` from Traefik means the router did not match the Host header.

## 8. Prove DNS, with routing out of the picture

Ask the controller's resolver directly. Use the host part of `UNIFI_HOST`:

```sh
dig @192.0.2.1 "$APP_HOST" A +short
```

Expected: your `TRAEFIK_ADDRESS`. Read the two results together:

| `curl --resolve` | `dig @controller` | Meaning |
|---|---|---|
| works | returns `TRAEFIK_ADDRESS` | Both work. Clients using UniFi DNS can now find Traefik. |
| works | empty | Routing is fine; the record is missing. Check the Bollard logs. |
| fails | returns address | DNS is fine; Traefik routing or the backend is broken. |
| works | returns another IP | A record you did not create already holds the name. Bollard preserves it and logs a conflict. |

You can also see the record in UniFi under the site's DNS records.

## 9. (Optional) Serve LAN clients

The loopback defaults suit a laptop. To serve real clients, set in `.env`:

```sh
HTTP_BIND=0.0.0.0
HTTP_PORT=80
TRAEFIK_ADDRESS=192.0.2.10 # Replace with this host's client-reachable LAN IPv4.
```

Re-render and restart, then test from another machine that uses UniFi DNS:

```sh
docker compose up -d --force-recreate
curl -i "http://$APP_HOST/"
```

Run `docker compose up -d --force-recreate` after changing either `.env` or a template. Changes to values such as `UNIFI_HOST` and `UNIFI_SITE` affect the render job, but do not necessarily recreate Traefik: its static configuration is stored in a named volume. Force recreation rerenders the files and restarts Traefik to load them. Bollard will not change a record that already exists, so if you changed `TRAEFIK_ADDRESS` after the record was created, edit that record in UniFi yourself.

## 10. Clean up

```sh
docker compose down -v
```

This removes the containers and the rendered-config volume. In releases after `v0.7.2`, stopping Traefik also stops the plugin, which deletes the DNS records it created, so `app.home.arpa` is removed from UniFi. With `v0.7.2` or earlier, remove it in UniFi yourself. Records you created by hand, or that earlier runs created, are left untouched.

## What you learned

- The plugin reads Traefik's own router API over a private loopback entrypoint.
- Only exact positive `Host()` names become records, all with TTL 300 and the single `address` you configured.
- `curl --resolve` tests routing; `dig @controller` tests DNS. Use both.

Next: [integrate file and Docker routes](how-to.md#discover-file-defined-routes), [serve HTTPS](how-to.md#serve-application-https), or read [why the plugin never updates and deletes only its own records](explanation.md#why-it-never-updates-and-deletes-only-its-own-records).
