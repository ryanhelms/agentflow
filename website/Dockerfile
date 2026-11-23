FROM node:24-slim

WORKDIR /home/node
USER node
COPY --chown=node:node package.json package-lock.json ./
COPY --chown=node:node vendor ./vendor
RUN --mount=type=cache,target=/home/node/.npm,uid=1000,gid=1000 npm ci --no-audit --no-fund

COPY --chown=node:node vite.config.js ./
COPY --chown=node:node src ./src
EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]
