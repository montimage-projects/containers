# ---------- build stage ----------
FROM node:20-alpine AS builder

WORKDIR /app

COPY package.json .
COPY app.js       .

RUN npm install --production


# ---------- final stage ----------
FROM ghcr.io/montimage/mmt-probe:v1.6.1

WORKDIR /app

EXPOSE 4000

COPY --from=builder /app /app