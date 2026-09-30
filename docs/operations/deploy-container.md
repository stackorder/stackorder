---
description: 'Run the Stackorder server as one distroless container plus Postgres on any platform: image tags, docker run, Docker Compose, other platforms and Postgres.'
---

# Deploy as a container

The server is one image, `ghcr.io/stackorder/stackorder`, plus a Postgres database. It runs on any platform that can run a container behind HTTPS.

## The image {#image}

- Tags: `X.Y.Z` for each release, and `latest`. Pin `X.Y.Z` in production.
- Built from `gcr.io/distroless/static:nonroot`: no shell, runs as a non-root user.
- Works with a read-only root filesystem. Nothing on local disk matters, so no volume is needed.
- Listens on port 8080 (`STACKORDER_LISTEN`), plain HTTP. Terminate TLS in front of it.
- Declares `HEALTHCHECK CMD ["/stackorder-server", "healthcheck"]`, which requests `/healthz` on the listen port, so `docker ps` shows the container's health. Platforms that ignore image health checks, such as ECS and Kubernetes, can run the same command.
- The web UI is embedded; there is no separate frontend to deploy.

## `docker run` {#docker-run}

```sh
docker run -d --name stackorder --read-only -p 8080:8080 \
  -e DATABASE_URL='postgres://stackorder:change-me@db.internal:5432/stackorder?sslmode=require' \
  -e STACKORDER_BASE_URL='https://stackorder.example.com' \
  -e STACKORDER_SESSION_KEY="$(cat session.key)" \
  -e GITHUB_APP_ID="$(cat app-id)" \
  -e GITHUB_APP_PRIVATE_KEY="$(cat stackorder-app.pem)" \
  -e GITHUB_WEBHOOK_SECRET="$(cat webhook-secret)" \
  -e GITHUB_OAUTH_CLIENT_ID="$(cat oauth-client-id)" \
  -e GITHUB_OAUTH_CLIENT_SECRET="$(cat oauth-client-secret)" \
  ghcr.io/stackorder/stackorder:latest
```

Leave out the five `GITHUB_*` variables on the first start: the server comes up in setup mode, and `/setup` creates the App and prints them. `/setup` needs the one-time token the server logs at start-up; take the full URL from the log:

```sh
docker logs stackorder 2>&1 | grep setup_url
```

With Compose, `docker compose logs stackorder | grep setup_url` shows the same line. See [Getting started](/guide/getting-started#create-app) and [the setup token](/reference/server-configuration#setup-token).

## Docker Compose {#compose}

```yaml
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: stackorder
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: stackorder
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U stackorder"]
      interval: 5s
      timeout: 3s
      retries: 10

  stackorder:
    image: ghcr.io/stackorder/stackorder:latest
    read_only: true
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgres://stackorder:${POSTGRES_PASSWORD}@postgres:5432/stackorder?sslmode=disable
      STACKORDER_BASE_URL: https://stackorder.example.com
      STACKORDER_SESSION_KEY: ${STACKORDER_SESSION_KEY}
      GITHUB_APP_ID: ${GITHUB_APP_ID}
      GITHUB_APP_PRIVATE_KEY: ${GITHUB_APP_PRIVATE_KEY}
      GITHUB_WEBHOOK_SECRET: ${GITHUB_WEBHOOK_SECRET}
      GITHUB_OAUTH_CLIENT_ID: ${GITHUB_OAUTH_CLIENT_ID}
      GITHUB_OAUTH_CLIENT_SECRET: ${GITHUB_OAUTH_CLIENT_SECRET}

volumes:
  pgdata:
```

Compose reads the `${...}` values from the shell or from a `.env` file next to the Compose file. Put a TLS-terminating reverse proxy in front of port 8080.

The `docker-compose.yml` at the repository root is for development: it starts Postgres and LocalStack for tests, and the server only with the `server` profile.

## Other platforms {#platforms}

Kubernetes, Nomad, Cloud Run, a VM with systemd: the requirements are the same.

| Requirement | Detail |
| --- | --- |
| Configuration | Environment variables only. Load secrets from the platform's secret store. See [Server configuration](/reference/server-configuration). |
| Inbound | HTTPS from GitHub to `/webhooks/github`, and from people to the UI. |
| Outbound | HTTPS to the GitHub API and to GitHub's OIDC key endpoint, and the Postgres port. Nothing else, unless the artifact bucket or tracing is enabled. |
| Health checks | Liveness on `GET /healthz`, readiness on `GET /readyz` (the database is reachable). |
| Replicas | One or two. All coordination goes through Postgres; every replica needs the same `STACKORDER_SESSION_KEY`. |
| Rollouts | Start the new replica and wait for `/readyz` before stopping the old one. Migrations run at start-up under a lock. |
| Resources | 0.25 vCPU and 512 MB serve an org with a few hundred stacks. |

A Kubernetes Deployment, for example:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: stackorder
spec:
  replicas: 2
  selector:
    matchLabels: { app: stackorder }
  template:
    metadata:
      labels: { app: stackorder }
    spec:
      containers:
        - name: stackorder
          image: ghcr.io/stackorder/stackorder:latest
          ports:
            - containerPort: 8080
          envFrom:
            - secretRef: { name: stackorder }
          env:
            - name: STACKORDER_BASE_URL
              value: https://stackorder.example.com
          livenessProbe:
            httpGet: { path: /healthz, port: 8080 }
          readinessProbe:
            httpGet: { path: /readyz, port: 8080 }
          resources:
            requests: { cpu: 250m, memory: 512Mi }
          securityContext:
            runAsNonRoot: true
            runAsUser: 65532
            runAsGroup: 65532
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
```

Here the Secret `stackorder` holds `DATABASE_URL`, `STACKORDER_SESSION_KEY` and the five `GITHUB_*` values. The image declares its user by name, `nonroot`, and Kubernetes can only verify `runAsNonRoot` against a numeric user, so the example sets `runAsUser` to the distroless `nonroot` id, 65532.

## Postgres {#postgres}

- **One database, one owner role.** The server runs its migrations at start-up, so its role must be able to create and alter tables in the database.
- **A DSN in `DATABASE_URL`**, such as `postgres://stackorder:secret@db.internal:5432/stackorder?sslmode=require`. Use TLS to a database outside the host. The connection pool gets `STACKORDER_WORKERS` plus 8 connections; add `pool_max_conns` to size it yourself. The scheduler's leader lock holds one connection.
- **A current version.** The development setup, the tests and the Terraform module default use Postgres 17, and the module accepts 14 or later. The schema relies on `gen_random_uuid()`, `SELECT ... FOR UPDATE SKIP LOCKED` and advisory locks.
- **Small.** A 300-stack monorepo's graph is well under a megabyte, and old plan text, queue rows and drift history are pruned. `db.t4g.micro` on RDS is enough to start.
- **Backed up.** It is the only stateful dependency. Point-in-time recovery is the simplest protection. See [Upgrades and backups](./upgrades-and-backups).

On AWS, the [Terraform module](./deploy-aws) creates all of this for you.
