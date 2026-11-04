# Public-Tier Deployment Guidance (#493)

Type: guide · Status: stable · Introduced: v0.9.119

`network.tier: public` is **not** built into phase 1. Exposing the gateway
beyond the LAN means TLS termination at a reverse proxy, and the gateway
deliberately does not ship built-in TLS (single binary, local files only).
This guide is the supported path.

## Reference topology

```
Internet → Caddy/nginx (TLS 443) → fusion-gateway 127.0.0.1:11432 (tier=lan)
```

The gateway keeps binding `127.0.0.1` (or the proxy-facing interface); the
proxy terminates TLS and forwards over loopback. Never set
`network.tier: lan` + `server.host: 0.0.0.0` on a host with a public
interface — lan tier is for trusted LANs only.

## Caddy (automatic HTTPS)

```caddy
gw.example.com {
    reverse_proxy 127.0.0.1:11432

    # SSE streams must not be buffered or timed out mid-stream
    flush_interval -1
}
```

## nginx (manual certs)

```nginx
server {
    listen 443 ssl;
    server_name gw.example.com;

    ssl_certificate     /etc/nginx/certs/gw.crt;
    ssl_certificate_key /etc/nginx/certs/gw.key;

    location / {
        proxy_pass http://127.0.0.1:11432;
        proxy_http_version 1.1;

        # SSE: no buffering, long read timeout for idle stream frames
        proxy_buffering off;
        proxy_read_timeout 3600s;

        # websockets (/v1/realtime)
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

## Checklist before going public

1. `auth.enabled: true`, `auth.passthrough: false`, explicit keys + master
   key rotated away from any template value (C1 placeholder guard enforces).
2. `cors.allowed_origins` set to the exact public origins — never `*`.
3. `admin.*` credentials are strong, unique (R7 placeholder guard).
4. Rate limiting considered (`routing.rate_limit`) — public exposure gets
   internet-grade abuse; per-key limits are the #493 follow-up.
5. Observability on: `/metrics` scraped, request-log persistence on for
   per-identity audit.
6. `managed_settings` payload reviewed — it is served without a key by
   design (Ed25519 signature trust); confirm the payload carries nothing
   secret.
7. Health probes `/healthz` `/readyz` `/livez` stay anonymous for LB checks;
   every other surface requires a credential by the lan guard (which stays
   active — public tier runs the same gate as lan).
