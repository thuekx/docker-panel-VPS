// Test app of the SITE-01 harness (APP-02). Answers with what the build wrote and the user it runs as.
const fs = require('fs');
const http = require('http');
http.createServer((req, res) => {
  const built = fs.existsSync('built.txt') ? fs.readFileSync('built.txt', 'utf8') : 'not built';
  res.writeHead(200, { 'content-type': 'text/plain' });
  res.end(`node-app ok | ${built} | uid ${process.getuid()} | port ${process.env.PORT}\n`);
}).listen(Number(process.env.PORT) || 3000, '0.0.0.0');
