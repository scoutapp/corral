# Deliberately plain: NO proxy env, NO CA cert handling, no build-args.
# The whole point is that this stock Dockerfile builds and runs unmodified —
# the ~/bin/docker wrapper injects the mitmproxy CA at build time, and the
# firewall captures egress transparently at run time.
FROM node:20-slim
WORKDIR /app
COPY package.json ./
RUN npm install --omit=dev --no-audit --no-fund
COPY main.js ./
CMD ["node", "main.js"]
