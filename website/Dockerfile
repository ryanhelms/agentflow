FROM node:24-slim

WORKDIR /home/node/website
RUN chown node:node /home/node/website
USER node
COPY --chown=node:node website/package.json website/package-lock.json ./
COPY --chown=node:node website/vendor ./vendor
RUN --mount=type=cache,target=/home/node/.npm,uid=1000,gid=1000 npm ci --no-audit --no-fund

COPY --chown=node:node website/vite.config.js website/content.mjs website/content.test.mjs ./
COPY --chown=node:node website/src ./src
COPY --chown=node:node docs /home/node/docs
COPY --chown=node:node README.md CONTRIBUTING.md CHANGELOG.md SECURITY.md /home/node/
EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]
