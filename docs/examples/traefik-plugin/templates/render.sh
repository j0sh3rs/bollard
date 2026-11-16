#!/bin/sh
# Render Traefik config from environment values. Runs in the one-shot "render" service.
set -eu

for name in UNIFI_HOST UNIFI_SITE UNIFI_SKIP_TLS_VERIFY UNIFI_CA_CERT TRAEFIK_ADDRESS \
  APP_HOST POLL_INTERVAL BOLLARD_VERSION PLUGIN_SOURCE; do
  eval "value=\${$name-}"
  case $value in
    *"
"*|*"$(printf '\r')"*)
      echo "render: $name must be a single line" >&2
      exit 1 ;;
    *'|'*|*'&'*|*'\'*|*'"'*|*'`'*)
      echo "render: $name contains a forbidden character (| & \\ \" \`)" >&2
      exit 1 ;;
  esac
  if [ -z "$value" ] && [ "$name" != UNIFI_CA_CERT ]; then
    echo "render: $name must not be empty" >&2
    exit 1
  fi
done
case $UNIFI_SKIP_TLS_VERIFY in true|false) ;; *)
  echo "render: UNIFI_SKIP_TLS_VERIFY must be true or false" >&2; exit 1 ;;
esac
case $PLUGIN_SOURCE in remote|local) ;; *)
  echo "render: PLUGIN_SOURCE must be remote or local" >&2; exit 1 ;;
esac

subst() {
  sed \
    -e "s|@UNIFI_HOST@|$UNIFI_HOST|g" \
    -e "s|@UNIFI_SITE@|$UNIFI_SITE|g" \
    -e "s|@UNIFI_CA_CERT@|$UNIFI_CA_CERT|g" \
    -e "s|@UNIFI_SKIP_TLS_VERIFY@|$UNIFI_SKIP_TLS_VERIFY|g" \
    -e "s|@TRAEFIK_ADDRESS@|$TRAEFIK_ADDRESS|g" \
    -e "s|@APP_HOST@|$APP_HOST|g" \
    -e "s|@POLL_INTERVAL@|$POLL_INTERVAL|g" \
    -e "s|@BOLLARD_VERSION@|$BOLLARD_VERSION|g"
}

mkdir -p /rendered/dynamic
cat /templates/static.yml.tmpl "/templates/plugin-$PLUGIN_SOURCE.yml.tmpl" | subst > /rendered/traefik.yml.new
mv /rendered/traefik.yml.new /rendered/traefik.yml
subst < /templates/dynamic.yml.tmpl > /rendered/dynamic/dynamic.yml
echo "render: wrote /rendered/traefik.yml and /rendered/dynamic/dynamic.yml (plugin source: $PLUGIN_SOURCE)"
