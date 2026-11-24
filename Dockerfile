FROM node:22-alpine
# git + ansible for playbooks from repositories; openssh-client is what ansible connects with
RUN apk add --no-cache git openssh-client ansible-core
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY . .
ENV HOST=0.0.0.0 DATA_DIR=/data NODE_ENV=production
RUN mkdir -p /data && chown node:node /data
VOLUME /data
EXPOSE 8080
USER node
CMD ["node", "--disable-warning=ExperimentalWarning", "server.js"]
