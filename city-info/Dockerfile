FROM node:20-alpine

WORKDIR /app

COPY package.json .
COPY app.js       .

# Install node modules
RUN npm install --production

EXPOSE 4000

# Container only provides files; it does not run the app
CMD ["node", "app.js"]