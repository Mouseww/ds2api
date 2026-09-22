# ds2api-login-service
# Headless-Chromium based DeepSeek login service.

FROM mcr.microsoft.com/playwright:v1.57.0-noble

WORKDIR /app

COPY package.json ./
RUN npm install --omit=dev

COPY index.js ./

ENV LOGIN_SERVICE_HOST=0.0.0.0
ENV LOGIN_SERVICE_PORT=8787

EXPOSE 8787

# Headed Chromium (needed to pass CloudFront/WAF) runs under a virtual
# display. The mcr.microsoft.com/playwright image ships xvfb-run for this.
CMD ["xvfb-run", "-a", "node", "index.js"]
