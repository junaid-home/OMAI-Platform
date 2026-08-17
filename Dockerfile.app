# syntax=docker/dockerfile:1.7

# Build the Portal from the committed minimal workspace graph and lockfile.
FROM node:24.19.0-bookworm-slim AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential ca-certificates git pkg-config python3 \
    && npm install -g bun@1.3.14 \
    && rm -rf /root/.npm \
    && rm -rf /var/lib/apt/lists/*

COPY ./ ./

RUN bun install --frozen-lockfile

# The SDK under services/ isn't a bun workspace member, so bun doesn't install
# its dependencies.  Bootstrap it explicitly so Rollup can resolve its imports.
RUN npm install --prefix services/omai-control-plane/sdk/typescript

# Build the frontend (values passed from docker-compose via .env)
ARG VITE_OMAI_API_BASE_URL
ARG VITE_OMAI_API_TOKEN
ARG VITE_OMAI_VOICE_GATEWAY_URL
RUN NODE_ENV=production \
    VITE_OMAI_API_BASE_URL="$VITE_OMAI_API_BASE_URL" \
    VITE_OMAI_API_TOKEN="$VITE_OMAI_API_TOKEN" \
    VITE_OMAI_VOICE_GATEWAY_URL="$VITE_OMAI_VOICE_GATEWAY_URL" \
    bun run --cwd packages/app build

FROM nginx:1.28.3-alpine

RUN rm /etc/nginx/conf.d/default.conf

COPY --from=builder /app/packages/app/dist /usr/share/nginx/html

COPY nginx.conf /etc/nginx/conf.d/default.conf

CMD ["nginx", "-g", "daemon off;"]
