# Built INSIDE DinD by the e2e test. The `npm install` step fetches from
# registry.npmjs.org over HTTPS, which only succeeds if the mitmproxy CA is
# trusted in this build — injected automatically by the sandbox's bin/docker
# wrapper (COPY --from=corral-ca + per-FROM env vars). That makes this
# Dockerfile a live test of the DinD build-time CA injection path.
FROM node:20-slim

WORKDIR /app
COPY package.json ./
RUN npm install --no-audit --no-fund
COPY server.js ./

EXPOSE 3000
CMD ["node", "server.js"]
